const fallbackViewport = { width: 800, height: 600 }
const svgNumber = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i
const svgLength = /^([+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?)(?:px)?$/i

function absoluteLength(value: string | null): number | undefined {
  const match = value?.trim().match(svgLength)
  if (!match) return undefined
  const length = Number(match[1])
  return Number.isFinite(length) && length > 0 ? length : undefined
}

// Read intrinsic geometry only. The detached document is never inserted into
// the page. Unknown/multiple canvases use a bounded viewport; arbitrary author
// CSS layouts cannot be inferred without executing or changing the artwork.
export function pelicanPreviewViewport(html?: string | null): { width: number; height: number } {
  if (!html || html.length > 2_000_000) return fallbackViewport
  try {
    const document = new DOMParser().parseFromString(html, 'text/html')
    const roots = [...document.querySelectorAll('svg')].filter(svg => !svg.parentElement?.closest('svg'))
    if (roots.length !== 1) return fallbackViewport
    const svg = roots[0]
    if (svg.closest('[hidden]')) return fallbackViewport

    const viewBox = svg.getAttribute('viewBox')
    let width = absoluteLength(svg.getAttribute('width'))
    let height = absoluteLength(svg.getAttribute('height'))
    if (viewBox !== null) {
      const box = viewBox.trim().split(/\s*,\s*|\s+/)
      if (box.length !== 4 || box.some(value => !svgNumber.test(value) || !Number.isFinite(Number(value)))) return fallbackViewport
      const boxWidth = Number(box[2]), boxHeight = Number(box[3])
      if (boxWidth <= 0 || boxHeight <= 0) return fallbackViewport
      // Preserve an explicit native viewport, even when its aspect differs
      // from the viewBox. The browser retains the author's letterboxing.
      if (width === undefined && height === undefined) {
        width = boxWidth
        height = boxHeight
      } else if (width === undefined) {
        width = height! * boxWidth / boxHeight
      } else if (height === undefined) {
        height = width * boxHeight / boxWidth
      }
    }
    if (width === undefined || height === undefined) return fallbackViewport
    const ratio = width / height
    // Bound the iframe's native allocation before applying the visual scale.
    if (width < 1 || height < 1 || width > 8192 || height > 8192 || width * height > 16_777_216 ||
      !Number.isFinite(ratio) || ratio < 1 / 20 || ratio > 20) return fallbackViewport
    return { width, height }
  } catch {
    return fallbackViewport
  }
}
