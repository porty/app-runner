export async function copyText(value: string): Promise<void> {
  let clipboardError: unknown
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value)
      return
    } catch (error) {
      clipboardError = error
    }
  }

  const input = document.createElement('textarea')
  input.value = value
  input.setAttribute('readonly', '')
  input.style.position = 'fixed'
  input.style.opacity = '0'
  document.body.appendChild(input)
  input.select()
  const copied = document.execCommand('copy')
  input.remove()
  if (!copied) {
    throw clipboardError instanceof Error ? clipboardError : new Error('The browser did not allow copying to the clipboard')
  }
}
