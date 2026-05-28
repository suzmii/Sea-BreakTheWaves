package chunk

import (
	"strings"
)

// ParseMarkdownArticle 解析 Markdown 文本为 Article。
func ParseMarkdownArticle(articleID string, md, fallbackTitle string) (Article, error) {
	lines := strings.Split(md, "\n")
	a := Article{
		ArticleID: articleID,
		Sections:  []Section{{H2: "default", Blocks: []Block{}}},
	}

	var textBuf []string
	flushText := func() {
		if len(textBuf) == 0 {
			return
		}
		t := strings.TrimSpace(strings.Join(textBuf, "\n"))
		textBuf = nil
		if t == "" {
			return
		}
		idx := len(a.Sections) - 1
		a.Sections[idx].Blocks = append(a.Sections[idx].Blocks, Block{Type: "text", Text: t})
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)

		// Title (#)
		if strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "## ") {
			flushText()
			title := strings.TrimPrefix(line, "# ")
			if a.Title == "" {
				a.Title = title
			}
			continue
		}

		// Cover image (first image after title)
		imgURL := extractImageURL(line)
		if imgURL != "" && a.Cover == "" {
			a.Cover = imgURL
			continue
		}

		// Type tags
		if st := parseTypeTag(line); st != "" {
			a.TypeTags = append(a.TypeTags, st)
			continue
		}

		// Section heading (##)
		if strings.HasPrefix(line, "## ") {
			flushText()
			h2 := strings.TrimPrefix(line, "## ")
			a.Sections = append(a.Sections, Section{H2: h2})
			continue
		}

		// Image block
		if imgURL != "" {
			flushText()
			idx := len(a.Sections) - 1
			a.Sections[idx].Blocks = append(a.Sections[idx].Blocks, Block{Type: "image", ImageURL: imgURL})
			continue
		}

		// Text line
		if line != "" {
			textBuf = append(textBuf, line)
		} else {
			flushText()
		}
	}
	flushText()

	if a.Title == "" {
		a.Title = fallbackTitle
	}
	if a.Title == "" {
		a.Title = "untitled"
	}

	// Remove default section if it has no content
	if len(a.Sections) == 1 && a.Sections[0].H2 == "default" && len(a.Sections[0].Blocks) == 0 {
		a.Sections = nil
	}

	a.ArticleID = NormalizeArticleID(articleID, a)
	return a, nil
}

func extractImageURL(line string) string {
	// ![alt](url)
	if idx := strings.Index(line, "!["); idx >= 0 {
		start := strings.Index(line[idx:], "](")
		if start >= 0 {
			end := strings.Index(line[idx+start:], ")")
			if end >= 0 {
				return line[idx+start+2 : idx+start+end]
			}
		}
	}
	// <img src="url">
	if idx := strings.Index(line, `<img src="`); idx >= 0 {
		start := idx + 10
		end := strings.Index(line[start:], `"`)
		if end >= 0 {
			return line[start : start+end]
		}
	}
	return ""
}

func parseTypeTag(line string) string {
	lower := strings.ToLower(strings.TrimSpace(line))
	if strings.HasPrefix(lower, "type:") {
		return strings.TrimSpace(line[5:])
	}
	if strings.HasPrefix(lower, "类型:") {
		return strings.TrimSpace(line[9:])
	}
	return ""
}
