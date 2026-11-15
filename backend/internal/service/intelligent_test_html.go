package service

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const intelligentPelicanHTMLLimit = 256 << 10

// Keep the document transparent so the white preview surface is only a fallback:
// setting html/body backgrounds here would mask the author's own canvas color.
// Zero-specificity rules in the first cascade layer precede the original styles,
// which remain authoritative even when the author uses cascade layers too.
// Keep root and nested SVG viewports intact; the frontend scales the original
// document viewport as a whole rather than changing the artwork's geometry.
const intelligentTestHTMLPreviewStyle = `<style data-sub2api-preview-defaults>@layer sub2api-preview-defaults{:where(html){color-scheme:light}:where(html,body){margin:0}:where(svg:not(svg svg)){display:block}}</style>`

var (
	intelligentBannedHTMLBlocks = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>`),
		regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</\s*iframe\s*>`),
		regexp.MustCompile(`(?is)<object\b[^>]*>.*?</\s*object\s*>`),
		regexp.MustCompile(`(?is)<embed\b[^>]*>.*?</\s*embed\s*>`),
		regexp.MustCompile(`(?is)<applet\b[^>]*>.*?</\s*applet\s*>`),
		regexp.MustCompile(`(?is)<form\b[^>]*>.*?</\s*form\s*>`),
	}
	intelligentBannedHTMLEmpty   = regexp.MustCompile(`(?is)<(script|iframe|object|embed|applet|form|input|button|textarea|select|link|base)\b[^>]*/?>`)
	intelligentHTMLEventAttr     = regexp.MustCompile(`(?i)\s+on[a-z][a-z0-9_-]*\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	intelligentHTMLJSURL         = regexp.MustCompile(`(?i)javascript\s*:`)
	intelligentHTMLScriptTag     = regexp.MustCompile(`(?i)<script\b`)
	intelligentHTMLCodeFence     = regexp.MustCompile("(?is)```(?:html|svg|xml)?[ \\t]*\\r?\\n(.*?)(?:\\r?\\n```|$)")
	intelligentHTMLRootTag       = regexp.MustCompile(`(?i)<(?:html|svg)(?:[\s/>]|$)`)
	errIntelligentHTMLIncomplete = errors.New("HTML 或 SVG 输出不完整")
)

func ExtractIntelligentTestHTMLDocument(output string) string {
	doc, _ := extractIntelligentTestHTMLDocument(output)
	return doc
}

func extractIntelligentTestHTMLDocument(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", errors.New("未返回 HTML 或 SVG")
	}
	fenced := false
	for _, fence := range intelligentHTMLCodeFence.FindAllStringSubmatch(trimmed, -1) {
		if intelligentHTMLRootTag.MatchString(fence[1]) {
			trimmed = strings.TrimSpace(fence[1])
			fenced = true
			break
		}
	}

	// Tokenize the original bytes rather than parsing and serializing a DOM: a
	// DOM parser repairs truncated markup, hiding an interrupted model response.
	// Tokenization also keeps closing tags in comments/scripts from satisfying
	// the document or nested SVG's required closing tag.
	z := html.NewTokenizer(strings.NewReader(trimmed))
	offset, firstTag, lastTagEnd := 0, -1, 0
	htmlStart, htmlEnd, doctypeStart := -1, -1, -1
	htmlDepth, svgDepth := 0, 0
	hasSVG, incomplete := false, false
tokens:
	for {
		typeID := z.Next()
		start := offset
		raw := z.Raw()
		offset += len(raw)
		switch typeID {
		case html.ErrorToken:
			// An unfinished opening root tag is not emitted as a start tag.
			incomplete = incomplete || intelligentHTMLRootTag.Match(raw)
			break tokens
		case html.DoctypeToken:
			doctype := strings.Fields(z.Token().Data)
			if htmlStart < 0 && len(doctype) > 0 && strings.EqualFold(doctype[0], "html") {
				doctypeStart = start
			}
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			if firstTag < 0 {
				firstTag = start
			}
			lastTagEnd = offset
			name, _ := z.TagName()
			switch string(name) {
			case "html":
				if typeID == html.EndTagToken {
					if htmlDepth == 0 {
						incomplete = true
						continue
					}
					htmlDepth--
					if htmlDepth == 0 {
						htmlEnd = offset
						break tokens
					}
				} else {
					if htmlStart < 0 {
						htmlStart = start
					}
					htmlDepth++
				}
			case "svg":
				if typeID == html.EndTagToken {
					if svgDepth == 0 {
						incomplete = true
					} else {
						svgDepth--
					}
				} else {
					hasSVG = true
					if typeID != html.SelfClosingTagToken {
						svgDepth++
					}
				}
			}
		}
	}
	if incomplete || htmlDepth != 0 || svgDepth != 0 {
		return "", errIntelligentHTMLIncomplete
	}
	if htmlStart >= 0 && htmlEnd > htmlStart {
		if doctypeStart >= 0 {
			return trimmed[doctypeStart:htmlEnd], nil
		}
		return "<!DOCTYPE html>" + trimmed[htmlStart:htmlEnd], nil
	}
	if hasSVG && firstTag >= 0 {
		// Keep sibling styles, wrapping containers and labels with the SVG.
		if fenced || firstTag == 0 {
			return wrapIntelligentTestHTML(trimmed), nil
		}
		return wrapIntelligentTestHTML(trimmed[firstTag:lastTagEnd]), nil
	}
	return "", errors.New("未返回 HTML 或 SVG")
}

func wrapIntelligentTestHTML(inner string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>` + inner + `</body></html>`
}

func addIntelligentTestHTMLPreviewDefaults(doc string) string {
	// Insert into the original bytes without reserializing or repairing the artwork.
	// Headless documents receive a head before the author's body/style content.
	z := html.NewTokenizer(strings.NewReader(doc))
	offset, htmlOpenEnd := 0, -1
	for {
		typeID := z.Next()
		offset += len(z.Raw())
		if typeID == html.ErrorToken {
			break
		}
		if typeID != html.StartTagToken {
			continue
		}
		name, _ := z.TagName()
		switch string(name) {
		case "html":
			if htmlOpenEnd < 0 {
				htmlOpenEnd = offset
			}
		case "head":
			return doc[:offset] + intelligentTestHTMLPreviewStyle + doc[offset:]
		case "body":
			if htmlOpenEnd >= 0 {
				return doc[:htmlOpenEnd] + "<head>" + intelligentTestHTMLPreviewStyle + "</head>" + doc[htmlOpenEnd:]
			}
		}
	}
	if htmlOpenEnd >= 0 {
		return doc[:htmlOpenEnd] + "<head>" + intelligentTestHTMLPreviewStyle + "</head>" + doc[htmlOpenEnd:]
	}
	return doc
}

func SanitizeIntelligentTestHTML(output string) (string, error) {
	doc, _, err := sanitizeIntelligentTestHTML(output)
	return doc, err
}

// PreviewIntelligentTestHTML returns a safe preview and a stable, public status.
// An empty issue means the preview is complete and no script behavior was removed.
func PreviewIntelligentTestHTML(output string) (string, string) {
	doc, scriptsRemoved, err := sanitizeIntelligentTestHTML(output)
	if errors.Is(err, errIntelligentHTMLIncomplete) {
		return "", "incomplete"
	}
	if err != nil {
		return "", "unavailable"
	}
	if scriptsRemoved {
		return doc, "scripts_removed"
	}
	return doc, ""
}

func sanitizeIntelligentTestHTML(output string) (string, bool, error) {
	doc, err := extractIntelligentTestHTMLDocument(output)
	if err != nil {
		return "", false, err
	}
	doc = addIntelligentTestHTMLPreviewDefaults(doc)
	if len(doc) > intelligentPelicanHTMLLimit {
		return "", false, errors.New("HTML 超过 256 KiB 上限")
	}
	scriptsRemoved := intelligentHTMLScriptTag.MatchString(doc) || intelligentHTMLEventAttr.MatchString(doc) || intelligentHTMLJSURL.MatchString(doc)
	for _, re := range intelligentBannedHTMLBlocks {
		doc = re.ReplaceAllString(doc, "")
	}
	doc = intelligentBannedHTMLEmpty.ReplaceAllString(doc, "")
	doc = intelligentHTMLEventAttr.ReplaceAllString(doc, "")
	doc = intelligentHTMLJSURL.ReplaceAllString(doc, "")
	if !strings.Contains(strings.ToLower(doc), "<svg") {
		return "", false, errors.New("HTML 中没有 SVG")
	}
	return doc, scriptsRemoved, nil
}
