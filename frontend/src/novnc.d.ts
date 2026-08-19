declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    constructor(target: Element, url: string, options?: { shared?: boolean })
    scaleViewport: boolean
    resizeSession: boolean
    focusOnClick: boolean
    disconnect(): void
    sendCtrlAltDel(): void
  }
}
