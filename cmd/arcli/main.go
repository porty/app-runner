// Command arcli controls an App Runner server through its public Twirp API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"gopkg.in/yaml.v3"
)

const defaultServer = "http://127.0.0.1:8080"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "arcli:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	global := flag.NewFlagSet("arcli", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	server := global.String("server", defaultServer, "App Runner server URL")
	timeout := global.Duration("timeout", 30*time.Second, "request timeout")
	format := global.String("o", "table", "output format: table, json, or yaml")
	if err := global.Parse(args); errors.Is(err, flag.ErrHelp) {
		usage(output)
		return nil
	} else if err != nil {
		return err
	}
	if *format != "table" && *format != "json" && *format != "yaml" {
		return fmt.Errorf("-o must be table, json, or yaml, not %q", *format)
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(output)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	out := &printer{Writer: output, format: *format}
	client := apprunnerv1.NewAppRunnerServiceJSONClient(strings.TrimRight(*server, "/"), http.DefaultClient)
	switch args[0] {
	case "host":
		return host(ctx, client, args[1:], out)
	case "iso":
		return iso(ctx, client, args[1:], out)
	case "image":
		return image(ctx, client, args[1:], out)
	case "storage":
		return storage(ctx, client, args[1:], out)
	case "vm":
		return vm(ctx, client, args[1:], out)
	case "net":
		return network(ctx, client, args[1:], out)
	case "cloud-init":
		return cloudInit(ctx, client, args[1:], out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(output io.Writer) {
	fmt.Fprint(output, `Usage: arcli [--server URL] [--timeout DURATION] [-o table|json|yaml] <command>

Commands:
  host status                    Show host capabilities
  iso list | image list          List installation ISOs or disk images
  storage list | download        Inspect storage or download a file by URL
  vm <operation>                 Manage virtual machines
  net <operation>                Inspect and manage host networking
  cloud-init <operation>         Manage reusable cloud-init templates

Run "arcli <command> help" for a command's operations. Successful output is a
table by default; pass -o json or -o yaml for machine-readable output.
`)
}

type printer struct {
	io.Writer
	format string
}

func write(output *printer, value any) error {
	switch output.format {
	case "json":
		return json.NewEncoder(output).Encode(value)
	case "yaml":
		return writeYAML(output, value)
	default:
		return writeTable(output, value)
	}
}

// writeYAML round-trips value through its JSON encoding so YAML output uses
// the same field names as the JSON output.
func writeYAML(output io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var plain any
	if err := decoder.Decode(&plain); err != nil {
		return err
	}
	data, err := yaml.Marshal(yamlNumbers(plain))
	if err != nil {
		return err
	}
	_, err = output.Write(data)
	return err
}

// yamlNumbers replaces json.Number values, which YAML would render as
// strings, with plain integers or floats.
func yamlNumbers(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, element := range value {
			value[key] = yamlNumbers(element)
		}
		return value
	case []any:
		for index, element := range value {
			value[index] = yamlNumbers(element)
		}
		return value
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer
		}
		if float, err := value.Float64(); err == nil {
			return float
		}
		return value.String()
	default:
		return value
	}
}

func writeTable(output io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	switch encoded = bytes.TrimSpace(encoded); {
	case bytes.HasPrefix(encoded, []byte("[")):
		var items []json.RawMessage
		if err := json.Unmarshal(encoded, &items); err != nil {
			return err
		}
		return writeListTable(output, items)
	default:
		fields, err := objectFields(encoded)
		if err != nil {
			return err
		}
		width := 0
		for _, field := range fields {
			width = max(width, len(field[0]))
		}
		for _, field := range fields {
			if _, err := fmt.Fprintln(output, tableRow([]string{field[0], field[1]}, []int{width})); err != nil {
				return err
			}
		}
		return nil
	}
}

func writeListTable(output io.Writer, items []json.RawMessage) error {
	var columns []string
	seen := map[string]bool{}
	rows := make([]map[string]string, len(items))
	for index, item := range items {
		fields, err := objectFields(bytes.TrimSpace(item))
		if err != nil {
			return err
		}
		rows[index] = map[string]string{}
		for _, field := range fields {
			if !seen[field[0]] {
				seen[field[0]] = true
				columns = append(columns, field[0])
			}
			rows[index][field[0]] = field[1]
		}
	}
	if len(columns) == 0 {
		return nil
	}
	widths := make([]int, len(columns))
	for index, column := range columns {
		widths[index] = len(column)
	}
	for _, row := range rows {
		for index, column := range columns {
			widths[index] = max(widths[index], len(row[column]))
		}
	}
	header := make([]string, len(columns))
	for index, column := range columns {
		header[index] = strings.ToUpper(column)
	}
	if _, err := fmt.Fprintln(output, tableRow(header, widths)); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for index, column := range columns {
			cells[index] = row[column]
		}
		if _, err := fmt.Fprintln(output, tableRow(cells, widths)); err != nil {
			return err
		}
	}
	return nil
}

func tableRow(cells []string, widths []int) string {
	padded := make([]string, len(cells))
	for index, value := range cells {
		if index < len(cells)-1 {
			value = fmt.Sprintf("%-*s", widths[index], value)
		}
		padded[index] = value
	}
	return strings.Join(padded, "  ")
}

// objectFields lists a JSON object's fields in declaration order; any other
// JSON value becomes a single "value" field.
func objectFields(encoded []byte) ([][2]string, error) {
	if len(encoded) == 0 || encoded[0] != '{' {
		return [][2]string{{"value", cell(encoded)}}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var fields [][2]string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields = append(fields, [2]string{fmt.Sprint(key), cell(value)})
	}
	return fields, nil
}

func cell(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
	}
	return string(raw)
}
func require(args []string, count int, message string) error {
	if len(args) < count {
		return errors.New(message)
	}
	return nil
}
func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

func host(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) != 1 || args[0] != "status" {
		return errors.New("usage: arcli host status")
	}
	r, err := c.GetHostStatus(ctx, &apprunnerv1.GetHostStatusRequest{})
	if err != nil {
		return err
	}
	return write(output, r.GetStatus())
}

func iso(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) != 1 || args[0] != "list" {
		return errors.New("usage: arcli iso list")
	}
	r, err := c.ListISOs(ctx, &apprunnerv1.ListISOsRequest{})
	if err != nil {
		return err
	}
	return write(output, r.GetImages())
}

func image(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) != 1 || args[0] != "list" {
		return errors.New("usage: arcli image list")
	}
	r, err := c.ListDiskImages(ctx, &apprunnerv1.ListDiskImagesRequest{})
	if err != nil {
		return err
	}
	return write(output, r.GetImages())
}

func storage(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(output, "usage: arcli storage list | download --root iso|disk --directory PATH --url URL [--filename NAME]")
		return err
	}
	switch args[0] {
	case "list":
		r, err := c.ListStorage(ctx, &apprunnerv1.ListStorageRequest{})
		if err != nil {
			return err
		}
		return write(output, r.GetRoots())
	case "download":
		f := flags("storage download")
		root := f.String("root", "", "storage root (iso or disk)")
		directory := f.String("directory", "", "destination directory")
		url := f.String("url", "", "source URL")
		filename := f.String("filename", "", "destination filename")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *url == "" {
			return errors.New("--root and --url are required")
		}
		r, err := c.DownloadStorageFile(ctx, &apprunnerv1.DownloadStorageFileRequest{Root: *root, Directory: *directory, Url: *url, Filename: *filename})
		if err != nil {
			return err
		}
		return write(output, r.GetFile())
	default:
		return fmt.Errorf("unknown storage operation %q", args[0])
	}
}

func vm(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(output, "usage: arcli vm list|get|create|clone|start|stop|reboot|delete|update|network|disk|cdrom|cloud-init|ipmi")
		return err
	}
	switch args[0] {
	case "list":
		r, e := c.ListVMs(ctx, &apprunnerv1.ListVMsRequest{})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachines())
	case "get":
		if e := require(args[1:], 1, "usage: arcli vm get ID"); e != nil {
			return e
		}
		r, e := c.GetVM(ctx, &apprunnerv1.GetVMRequest{Id: args[1]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	case "create":
		return createVM(ctx, c, args[1:], output)
	case "clone":
		if e := require(args[1:], 2, "usage: arcli vm clone ID NAME"); e != nil {
			return e
		}
		r, e := c.CloneVM(ctx, &apprunnerv1.CloneVMRequest{Id: args[1], Name: args[2]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	case "start", "reboot", "delete":
		return vmAction(ctx, c, args, output)
	case "stop":
		f := flags("vm stop")
		force := f.Bool("force", false, "force stop")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if e := require(f.Args(), 1, "usage: arcli vm stop [--force] ID"); e != nil {
			return e
		}
		r, e := c.StopVM(ctx, &apprunnerv1.StopVMRequest{Id: f.Args()[0], Force: *force})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	case "update":
		return updateVM(ctx, c, args[1:], output)
	case "network":
		return vmNetwork(ctx, c, args[1:], output)
	case "disk":
		return vmDisk(ctx, c, args[1:], output)
	case "cdrom":
		return vmCDROM(ctx, c, args[1:], output)
	case "cloud-init":
		return vmCloudInit(ctx, c, args[1:], output)
	case "ipmi":
		return vmIPMI(ctx, c, args[1:], output)
	default:
		return fmt.Errorf("unknown VM operation %q", args[0])
	}
}

func vmAction(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if e := require(args[1:], 1, "usage: arcli vm "+args[0]+" ID"); e != nil {
		return e
	}
	switch args[0] {
	case "start":
		r, e := c.StartVM(ctx, &apprunnerv1.StartVMRequest{Id: args[1]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	case "reboot":
		r, e := c.RebootVM(ctx, &apprunnerv1.RebootVMRequest{Id: args[1]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	default:
		r, e := c.DeleteVM(ctx, &apprunnerv1.DeleteVMRequest{Id: args[1]})
		if e != nil {
			return e
		}
		return write(output, r)
	}
}

func createVM(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	f := flags("vm create")
	name := f.String("name", "", "VM name")
	cpus := f.Uint("cpus", 1, "vCPUs")
	memory := f.Uint("memory", 1024, "memory MiB")
	disk := f.Uint("disk", 10, "system disk GiB")
	iso := f.String("iso", "", "ISO name")
	network := f.String("network", "nat", "nat or bridge")
	bridge := f.String("bridge", "", "bridge name")
	image := f.String("image", "", "source cloud image")
	cloud := f.String("cloud-init-file", "", "cloud-init user-data file")
	if e := f.Parse(args); e != nil {
		return e
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	mode, e := networkMode(*network)
	if e != nil {
		return e
	}
	userData, e := readOptional(*cloud)
	if e != nil {
		return e
	}
	r, e := c.CreateVM(ctx, &apprunnerv1.CreateVMRequest{Name: *name, Cpus: uint32(*cpus), MemoryMib: uint32(*memory), DiskGib: uint32(*disk), IsoName: *iso, NetworkMode: mode, BridgeName: *bridge, CloudInitEnabled: *cloud != "", CloudInitUserData: userData, SourceImage: *image})
	if e != nil {
		return e
	}
	return write(output, r.GetVirtualMachine())
}
func updateVM(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	f := flags("vm update")
	name := f.String("name", "", "VM name")
	description := f.String("description", "", "description")
	cpus := f.Uint("cpus", 0, "vCPUs")
	memory := f.Uint("memory", 0, "memory MiB")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := require(f.Args(), 1, "usage: arcli vm update ID --name NAME --cpus N --memory MIB"); e != nil {
		return e
	}
	r, e := c.UpdateVM(ctx, &apprunnerv1.UpdateVMRequest{Id: f.Args()[0], Name: *name, Description: *description, Cpus: uint32(*cpus), MemoryMib: uint32(*memory)})
	if e != nil {
		return e
	}
	return write(output, r.GetVirtualMachine())
}
func vmNetwork(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	f := flags("vm network")
	network := f.String("network", "nat", "nat or bridge")
	bridge := f.String("bridge", "", "bridge name")
	mac := f.String("mac", "", "MAC address")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := require(f.Args(), 1, "usage: arcli vm network ID --network nat|bridge [--bridge NAME] [--mac ADDRESS]"); e != nil {
		return e
	}
	mode, e := networkMode(*network)
	if e != nil {
		return e
	}
	r, e := c.UpdateVMNetwork(ctx, &apprunnerv1.UpdateVMNetworkRequest{Id: f.Args()[0], NetworkMode: mode, BridgeName: *bridge, MacAddress: *mac})
	if e != nil {
		return e
	}
	return write(output, r.GetVirtualMachine())
}
func vmDisk(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 {
		return errors.New("usage: arcli vm disk add ID --size GIB | remove ID DISK_ID")
	}
	if e := require(args[1:], 1, "VM ID is required"); e != nil {
		return e
	}
	if args[0] == "add" {
		f := flags("vm disk add")
		size := f.Uint("size", 0, "disk GiB")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if e := require(f.Args(), 1, "VM ID is required"); e != nil {
			return e
		}
		r, e := c.AddVMDisk(ctx, &apprunnerv1.AddVMDiskRequest{Id: f.Args()[0], SizeGib: uint32(*size)})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	}
	if args[0] == "remove" {
		if e := require(args[1:], 2, "usage: arcli vm disk remove ID DISK_ID"); e != nil {
			return e
		}
		r, e := c.RemoveVMDisk(ctx, &apprunnerv1.RemoveVMDiskRequest{Id: args[1], DiskId: args[2]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	}
	return errors.New("usage: arcli vm disk add|remove")
}
func vmCDROM(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 {
		return errors.New("usage: arcli vm cdrom add|set|remove")
	}
	switch args[0] {
	case "add":
		if e := require(args[1:], 1, "usage: arcli vm cdrom add ID [ISO]"); e != nil {
			return e
		}
		iso := ""
		if len(args) > 2 {
			iso = args[2]
		}
		r, e := c.AddVMCDROM(ctx, &apprunnerv1.AddVMCDROMRequest{Id: args[1], IsoName: iso})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	case "set":
		if e := require(args[1:], 3, "usage: arcli vm cdrom set ID CDROM_ID ISO"); e != nil {
			return e
		}
		r, e := c.UpdateVMCDROM(ctx, &apprunnerv1.UpdateVMCDROMRequest{Id: args[1], CdromId: args[2], IsoName: args[3]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	case "remove":
		if e := require(args[1:], 2, "usage: arcli vm cdrom remove ID CDROM_ID"); e != nil {
			return e
		}
		r, e := c.RemoveVMCDROM(ctx, &apprunnerv1.RemoveVMCDROMRequest{Id: args[1], CdromId: args[2]})
		if e != nil {
			return e
		}
		return write(output, r.GetVirtualMachine())
	default:
		return errors.New("usage: arcli vm cdrom add|set|remove")
	}
}
func vmCloudInit(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	f := flags("vm cloud-init")
	enabled := f.Bool("enabled", true, "enable cloud-init")
	file := f.String("file", "", "user-data file")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := require(f.Args(), 1, "usage: arcli vm cloud-init ID --enabled --file USER_DATA"); e != nil {
		return e
	}
	data, e := readOptional(*file)
	if e != nil {
		return e
	}
	r, e := c.ConfigureVMCloudInit(ctx, &apprunnerv1.ConfigureVMCloudInitRequest{Id: f.Args()[0], Enabled: *enabled, UserData: data})
	if e != nil {
		return e
	}
	return write(output, r.GetVirtualMachine())
}
func vmIPMI(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	f := flags("vm ipmi")
	enabled := f.Bool("enabled", true, "enable IPMI")
	bridge := f.String("bridge", "", "bridge name")
	address := f.String("address", "", "IP address")
	username := f.String("username", "", "username")
	password := f.String("password", "", "password")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := require(f.Args(), 1, "usage: arcli vm ipmi ID --enabled --bridge NAME --address IP --username USER --password PASS"); e != nil {
		return e
	}
	r, e := c.ConfigureVMIPMI(ctx, &apprunnerv1.ConfigureVMIPMIRequest{Id: f.Args()[0], Enabled: *enabled, BridgeName: *bridge, Address: *address, Username: *username, Password: *password})
	if e != nil {
		return e
	}
	return write(output, r.GetVirtualMachine())
}

func network(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(output, "usage: arcli net status|apply|confirm|revert|dhcp")
		return err
	}
	switch args[0] {
	case "status":
		r, e := c.GetNetworkingStatus(ctx, &apprunnerv1.GetNetworkingStatusRequest{})
		if e != nil {
			return e
		}
		return write(output, r.GetStatus())
	case "apply":
		f := flags("net apply")
		kind := f.String("type", "", "create-bridge, delete-bridge, up, down, attach, or detach")
		bridge := f.String("bridge", "", "bridge name")
		iface := f.String("interface", "", "interface name")
		migrate := f.Bool("migrate-addresses", false, "migrate addresses")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		typ, e := networkChange(*kind)
		if e != nil {
			return e
		}
		r, e := c.ApplyNetworkChange(ctx, &apprunnerv1.ApplyNetworkChangeRequest{Type: typ, BridgeName: *bridge, InterfaceName: *iface, MigrateAddresses: *migrate})
		if e != nil {
			return e
		}
		return write(output, r.GetPendingChange())
	case "confirm", "revert":
		if e := require(args[1:], 1, "network change ID is required"); e != nil {
			return e
		}
		if args[0] == "confirm" {
			r, e := c.ConfirmNetworkChange(ctx, &apprunnerv1.ConfirmNetworkChangeRequest{Id: args[1]})
			if e != nil {
				return e
			}
			return write(output, r.GetStatus())
		}
		r, e := c.RevertNetworkChange(ctx, &apprunnerv1.RevertNetworkChangeRequest{Id: args[1]})
		if e != nil {
			return e
		}
		return write(output, r.GetStatus())
	case "dhcp":
		return networkDHCP(ctx, c, args[1:], output)
	default:
		return fmt.Errorf("unknown network operation %q", args[0])
	}
}
func networkDHCP(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 {
		return errors.New("usage: arcli net dhcp configure|clear-leases|delete-lease|set-static")
	}
	if args[0] == "configure" {
		f := flags("net dhcp configure")
		enabled := f.Bool("enabled", true, "enable DHCP")
		cidr := f.String("cidr", "", "subnet CIDR")
		nat := f.Bool("nat", true, "enable NAT")
		dns := f.Bool("dns", true, "enable DNS")
		forwarders := f.String("dns-forwarders", "", "comma-separated DNS forwarders")
		autoDNS := f.Bool("auto-dns", true, "use host resolvers")
		suffix := f.String("dns-suffix", "", "DNS suffix")
		description := f.String("description", "", "description")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if e := require(f.Args(), 1, "bridge name is required"); e != nil {
			return e
		}
		r, e := c.ConfigureBridgeDHCP(ctx, &apprunnerv1.ConfigureBridgeDHCPRequest{BridgeName: f.Args()[0], Enabled: *enabled, Cidr: *cidr, NatEnabled: *nat, DnsEnabled: *dns, DnsForwarders: split(*forwarders), AutoDns: *autoDNS, DnsSuffix: *suffix, Description: *description})
		if e != nil {
			return e
		}
		return write(output, r.GetStatus())
	}
	if args[0] == "clear-leases" {
		if e := require(args[1:], 1, "bridge name is required"); e != nil {
			return e
		}
		r, e := c.ClearBridgeDHCPLeases(ctx, &apprunnerv1.ClearBridgeDHCPLeasesRequest{BridgeName: args[1]})
		if e != nil {
			return e
		}
		return write(output, r.GetStatus())
	}
	if args[0] == "delete-lease" || args[0] == "set-static" {
		if e := require(args[1:], 2, "usage: arcli net dhcp "+args[0]+" BRIDGE MAC [ADDRESS]"); e != nil {
			return e
		}
		if args[0] == "delete-lease" {
			r, e := c.DeleteBridgeDHCPLease(ctx, &apprunnerv1.DeleteBridgeDHCPLeaseRequest{BridgeName: args[1], HardwareAddress: args[2]})
			if e != nil {
				return e
			}
			return write(output, r.GetStatus())
		}
		if e := require(args[1:], 3, "address is required"); e != nil {
			return e
		}
		r, e := c.SetBridgeDHCPStaticLease(ctx, &apprunnerv1.SetBridgeDHCPStaticLeaseRequest{BridgeName: args[1], HardwareAddress: args[2], Address: args[3]})
		if e != nil {
			return e
		}
		return write(output, r.GetStatus())
	}
	return errors.New("usage: arcli net dhcp configure|clear-leases|delete-lease|set-static")
}

func cloudInit(ctx context.Context, c apprunnerv1.AppRunnerService, args []string, output *printer) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := fmt.Fprintln(output, "usage: arcli cloud-init list|create|update|delete")
		return err
	}
	switch args[0] {
	case "list":
		r, e := c.ListCloudInitTemplates(ctx, &apprunnerv1.ListCloudInitTemplatesRequest{})
		if e != nil {
			return e
		}
		return write(output, r.GetTemplates())
	case "create", "update":
		f := flags("cloud-init " + args[0])
		file := f.String("file", "", "user-data file")
		newName := f.String("new-name", "", "new template name")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if e := require(f.Args(), 1, "template name is required"); e != nil {
			return e
		}
		data, e := readOptional(*file)
		if e != nil {
			return e
		}
		if args[0] == "create" {
			r, e := c.CreateCloudInitTemplate(ctx, &apprunnerv1.CreateCloudInitTemplateRequest{Name: f.Args()[0], UserData: data})
			if e != nil {
				return e
			}
			return write(output, r.GetTemplate())
		}
		r, e := c.UpdateCloudInitTemplate(ctx, &apprunnerv1.UpdateCloudInitTemplateRequest{Name: f.Args()[0], NewName: *newName, UserData: data})
		if e != nil {
			return e
		}
		return write(output, r.GetTemplate())
	case "delete":
		if e := require(args[1:], 1, "template name is required"); e != nil {
			return e
		}
		r, e := c.DeleteCloudInitTemplate(ctx, &apprunnerv1.DeleteCloudInitTemplateRequest{Name: args[1]})
		if e != nil {
			return e
		}
		return write(output, r)
	default:
		return errors.New("usage: arcli cloud-init list|create|update|delete")
	}
}

func readOptional(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	return string(data), err
}
func split(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
func networkMode(value string) (apprunnerv1.NetworkMode, error) {
	switch value {
	case "nat":
		return apprunnerv1.NetworkMode_NETWORK_MODE_NAT, nil
	case "bridge":
		return apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE, nil
	default:
		return 0, errors.New("--network must be nat or bridge")
	}
}
func networkChange(value string) (apprunnerv1.NetworkChangeType, error) {
	values := map[string]apprunnerv1.NetworkChangeType{"create-bridge": apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_CREATE_BRIDGE, "delete-bridge": apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DELETE_BRIDGE, "up": apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_UP, "down": apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_DOWN, "attach": apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_ATTACH_INTERFACE, "detach": apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DETACH_INTERFACE}
	kind, ok := values[value]
	if !ok {
		return 0, errors.New("invalid network change type")
	}
	return kind, nil
}
