const fallbackViewport = { width: 800, height: 600 }
const svgNumber = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i
const svgLength = /^([+]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?)(?:px)?$/i

type CanvasElement = 'body' | 'html' | 'svg'
type Candidate<T> = { value: T; important: boolean; specificity: number; order: number }
type Background = { color?: Candidate<string | undefined>; image?: Candidate<boolean> }
const namedColors = new Set(('aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen').split(' '))

// A template's contents have an inert owner document: never append these nodes
// or their stylesheets. Unlike DOMParser's HTML document, this does not start
// image/iframe loads while inspecting model output.
function previewDocument(html: string) {
  const template = document.createElement('template')
  template.innerHTML = html
  const wrappers: Partial<Record<'html' | 'body', Element>> = {}
  // Fragment parsing omits html/body attributes. Recover only complete opening
  // tags, outside comments, raw-text elements, SVGs and nested templates.
  const tags = /<!--[\s\S]*?(?:-->|$)|<\/?([a-z][a-z0-9:-]*)\b(?:[^"'<>]|"[^"]*"|'[^']*')*>/gi
  let templateDepth = 0
  let svgDepth = 0
  for (let match = tags.exec(html); match; match = tags.exec(html)) {
    if (!match[1]) continue
    const name = match[1].toLowerCase()
    const closing = match[0].startsWith('</')
    if (name === 'template') templateDepth = Math.max(0, templateDepth + (closing ? -1 : 1))
    if (name === 'svg') svgDepth = Math.max(0, svgDepth + (closing ? -1 : 1))
    if (!closing && !templateDepth && !svgDepth && (name === 'html' || name === 'body') && !wrappers[name]) {
      const attributes = match[0].slice(name.length + 1, -1)
      const holder = document.createElement('template')
      holder.innerHTML = `<div${attributes}></div>`
      wrappers[name] = holder.content.firstElementChild ?? undefined
    }
    if (!closing && /^(script|style|textarea|title|xmp|iframe|noembed|noframes|noscript)$/.test(name)) {
      const end = new RegExp(`</${name}\\s*>`, 'gi')
      end.lastIndex = tags.lastIndex
      const close = end.exec(html)
      if (!close) break
      tags.lastIndex = end.lastIndex
    }
    if (!closing && name === 'plaintext') break
  }
  const roots = [...template.content.querySelectorAll('svg')].filter(svg => !svg.parentElement?.closest('svg'))
  const svg = roots.length === 1 ? roots[0] : undefined
  return { content: template.content, wrappers, svg }
}

// Only punctuation outside CSS strings/comments is structural. This allows us
// to skip entire at-rule/nested blocks instead of mistaking an inner body rule
// or quoted text for a top-level rule.
function cssTokens() {
  return /\/\*[\s\S]*?(?:\*\/|$)|"(?:\\[\s\S]|[^"\\])*(?:"|$)|'(?:\\[\s\S]|[^'\\])*(?:'|$)|[{}();]/g
}

function* simpleRules(css: string): Generator<{ selectors: string[]; declarations: string }> {
  const tokens = cssTokens()
  let start = 0, body = 0, depth = 0, parentheses = 0, nested = false
  let selector = ''
  for (let token = tokens.exec(css); token; token = tokens.exec(css)) {
    const symbol = token[0]
    if (symbol === '(') parentheses++
    else if (symbol === ')') parentheses = Math.max(0, parentheses - 1)
    else if (!parentheses && symbol === '{') {
      if (depth++ === 0) {
        selector = css.slice(start, token.index).replace(/\/\*[\s\S]*?\*\//g, ' ').trim().toLowerCase()
        body = tokens.lastIndex
        nested = false
      } else nested = true
    } else if (!parentheses && symbol === '}') {
      if (depth > 0 && --depth === 0) {
        const selectors = selector.split(',').map(value => value.trim())
        if (!nested && selectors.every(value => /^(html|body|svg|:root)$/.test(value))) {
          yield { selectors, declarations: css.slice(body, token.index) }
        }
        start = tokens.lastIndex
      }
    } else if (!parentheses && !depth && symbol === ';') start = tokens.lastIndex
  }
}

function* declarations(css: string): Generator<{ property: string; value: string; important: boolean }> {
  const tokens = cssTokens()
  let start = 0, parentheses = 0
  const parts: string[] = []
  for (let token = tokens.exec(css); token; token = tokens.exec(css)) {
    if (token[0] === '(') parentheses++
    else if (token[0] === ')') parentheses = Math.max(0, parentheses - 1)
    else if (token[0] === ';' && !parentheses) {
      parts.push(css.slice(start, token.index))
      start = tokens.lastIndex
    }
  }
  parts.push(css.slice(start))
  for (const part of parts) {
    const match = /^\s*(background(?:-color|-image)?)\s*:([\s\S]*)$/i.exec(part.replace(/\/\*[\s\S]*?\*\//g, ' '))
    if (!match) continue
    const important = /!\s*important\s*$/i.test(match[2])
    const value = match[2].replace(/!\s*important\s*$/i, '').trim().toLowerCase()
    if (value) yield { property: match[1].toLowerCase(), value, important }
  }
}

function opaqueColor(value: string, probe: CSSStyleDeclaration): string | undefined {
  if (/^#[\da-f]{3}(?:[\da-f]{3})?$/.test(value)) return value
  if (/^#[\da-f]{3}f$/.test(value) || /^#[\da-f]{6}ff$/.test(value)) return value
  if (namedColors.has(value)) return value
  const functional = /^(rgba?|hsla?)\(([\s\S]*)\)$/.exec(value)
  if (!functional) return undefined
  const channels = functional[2]
  const numeric = functional[1].startsWith('hsl') ? channels.replace(/(?:deg|grad|rad|turn)/g, '') : channels
  if (!/^[\de+.,%/\s-]+$/.test(numeric)) return undefined
  const slash = channels.split('/')
  const comma = channels.split(',')
  const alpha = slash.length === 2 ? slash[1].trim() : comma.length === 4 ? comma[3].trim() : undefined
  if (alpha !== undefined && (!/^[+\d.e-]+%?$/.test(alpha) ||
    Number(alpha.replace(/%$/, '')) !== (alpha.endsWith('%') ? 100 : 1))) return undefined
  probe.color = ''
  probe.color = value
  return probe.color ? value : undefined
}

function cascade<T>(previous: Candidate<T> | undefined, next: Candidate<T>): Candidate<T> {
  if (!previous || Number(next.important) > Number(previous.important) ||
    (next.important === previous.important && (next.specificity > previous.specificity ||
      (next.specificity === previous.specificity && next.order > previous.order)))) return next
  return previous
}

/** A conservative, opaque canvas color for the player's uncovered margins. */
export function pelicanPreviewBackground(html?: string | null): string | undefined {
  if (!html || html.length > 2_000_000) return undefined
  try {
    const parsed = previewDocument(html)
    const backgrounds: Record<CanvasElement, Background> = { body: {}, html: {}, svg: {} }
    const probe = document.createElement('div').style
    let order = 0
    const apply = (element: CanvasElement, css: string, specificity: number) => {
      for (const declaration of declarations(css)) {
        const rank = { important: declaration.important, specificity, order: ++order }
        const state = backgrounds[element]
        const color = opaqueColor(declaration.value, probe)
        if (declaration.property !== 'background-image') {
          // Unknown/relative values also win the cascade, but never become a
          // guessed color or resurrect an earlier overridden declaration.
          state.color = cascade(state.color, { ...rank, value: color })
        }
        if (declaration.property !== 'background-color') {
          const image = declaration.property === 'background-image'
            ? declaration.value !== 'none'
            : !color && declaration.value !== 'none' && declaration.value !== 'transparent'
          state.image = cascade(state.image, { ...rank, value: image })
        }
      }
    }
    for (const style of parsed.content.querySelectorAll('style')) {
      if (style.getAttribute('media')?.trim() && style.getAttribute('media')?.trim().toLowerCase() !== 'all') continue
      if (style.getAttribute('type')?.trim() && style.getAttribute('type')?.trim().toLowerCase() !== 'text/css') continue
      for (const rule of simpleRules(style.textContent ?? '')) {
        for (const element of ['body', 'html', 'svg'] as const) {
          const matching = rule.selectors.filter(selector => selector === element || (selector === ':root' && element === 'html'))
          if (matching.length) apply(element, rule.declarations, matching.includes(':root') ? 10 : 1)
        }
      }
    }
    for (const element of ['body', 'html', 'svg'] as const) {
      const node = element === 'svg' ? parsed.svg : parsed.wrappers[element]
      if (node) apply(element, node.getAttribute('style') ?? '', 1000)
    }
    for (const element of ['body', 'html', 'svg'] as const) {
      if (element === 'svg' && (!parsed.svg || parsed.svg.closest('[hidden]'))) continue
      const state = backgrounds[element]
      if (!state.image?.value && state.color?.value) return state.color.value
    }
  } catch {
    // Unsupported/malformed author CSS is not a reason to change the artwork.
  }
  return undefined
}

function absoluteLength(value: string | null): number | undefined {
  const match = value?.trim().match(svgLength)
  if (!match) return undefined
  const length = Number(match[1])
  return Number.isFinite(length) && length > 0 ? length : undefined
}

// Read intrinsic geometry only. The inert document is never inserted into
// the page. Unknown/multiple canvases use a bounded viewport; arbitrary author
// CSS layouts cannot be inferred without executing or changing the artwork.
export function pelicanPreviewViewport(html?: string | null): { width: number; height: number } {
  if (!html || html.length > 2_000_000) return fallbackViewport
  try {
    const { svg, wrappers } = previewDocument(html)
    if (!svg || svg.closest('[hidden]') || wrappers.html?.hasAttribute('hidden') || wrappers.body?.hasAttribute('hidden')) return fallbackViewport

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
