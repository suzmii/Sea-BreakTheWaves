package chunk

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// SplitArticle 将文章切分为 coarse text + fine chunks。
func SplitArticle(a Article, maxTokens, overlapTokens, keywordTopK int) (SplitResult, error) {
	keywords := detectKeywords(a, keywordTopK)
	kwScore := keywordCoverageScore(len(keywords))

	var coarseRaw strings.Builder
	fmt.Fprintf(&coarseRaw, "title: %s\n", a.Title)
	fmt.Fprintf(&coarseRaw, "type_tags: %s\n", strings.Join(a.TypeTags, ","))
	fmt.Fprintf(&coarseRaw, "keywords: %s\n", strings.Join(keywords, ","))
	fmt.Fprintf(&coarseRaw, "headings: %s\n", strings.Join(sectionH2s(a), " | "))

	intro := fmt.Sprintf("This article belongs to the categories of %s. "+
		"Its main keywords include: %s. The article covers the following topics: %s.",
		strings.Join(a.TypeTags, ", "),
		strings.Join(keywords, ", "),
		strings.Join(sectionH2s(a), ", "))

	coarseText := coarseRaw.String() + "\nintro: " + intro + fmt.Sprintf("\nkeyword_score: %.2f", kwScore)

	var fineChunks []Chunk
	seq := 1
	for _, sec := range a.Sections {
		var textParts []string
		for _, blk := range sec.Blocks {
			if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
				textParts = append(textParts, strings.TrimSpace(blk.Text))
			}
		}
		body := "heading: " + sec.H2 + "\n" + strings.Join(textParts, "\n")
		if strings.TrimSpace(body) == "" {
			continue
		}

		chunks := splitByTokenBudget(body, maxTokens, overlapTokens)
		for _, c := range chunks {
			fineChunks = append(fineChunks, Chunk{
				ChunkID:     BuildChunkID(a.ArticleID, seq, sec.H2, c),
				ArticleID:   a.ArticleID,
				H2:          sec.H2,
				Content:     c,
				Tokens:      approxTokenCount(c),
				ContentType: "text",
			})
			seq++
		}
	}

	return SplitResult{
		CoarseText:   coarseText,
		CoarseIntro:  intro,
		FineChunks:   fineChunks,
		Keywords:     keywords,
		KeywordScore: kwScore,
	}, nil
}

// detectKeywords 从文章元数据中提取关键词。
func detectKeywords(a Article, topK int) []string {
	score := make(map[string]float64)
	add := func(s string, weight float64) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		score[s] += weight
	}

	for _, t := range a.TypeTags {
		add(t, 5.0)
	}
	for _, t := range a.Tags {
		add(t, 4.0)
	}
	for _, s := range a.Sections {
		for _, kw := range extractHashtags(s.H2) {
			add(kw, 4.0)
		}
		for _, p := range keywordPieces(s.H2) {
			add(p, 2.0)
		}
		for _, blk := range s.Blocks {
			for _, kw := range extractHashtags(blk.Text) {
				add(kw, 4.0)
			}
		}
	}

	type kv struct {
		k string
		v float64
	}
	var sorted []kv
	for k, v := range score {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].v != sorted[j].v {
			return sorted[i].v > sorted[j].v
		}
		return sorted[i].k < sorted[j].k
	})
	if topK <= 0 {
		topK = 10
	}
	if topK > len(sorted) {
		topK = len(sorted)
	}
	out := make([]string, 0, topK)
	for _, kv := range sorted[:topK] {
		out = append(out, kv.k)
	}
	return out
}

func keywordCoverageScore(n int) float32 {
	if n >= 5 {
		return 1.0
	}
	return float32(n) * 0.2
}

func splitByTokenBudget(text string, maxTokens, overlapTokens int) []string {
	lines := strings.Split(text, "\n")
	var chunks []string
	var current strings.Builder
	currentTokens := 0

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
		}
		current.Reset()
		currentTokens = 0
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lineTokens := approxTokenCount(line)
		if lineTokens > maxTokens {
			flush()
			// Single long line: split by rune
			runes := []rune(line)
			step := maxTokens - overlapTokens
			if step <= 0 {
				step = maxTokens / 2
			}
			for i := 0; i < len(runes); i += step {
				end := i + maxTokens
				if end > len(runes) {
					end = len(runes)
				}
				chunk := string(runes[i:end])
				if i > 0 && overlapTokens > 0 {
					prevStart := i - overlapTokens
					if prevStart < 0 {
						prevStart = 0
					}
					chunk = string(runes[prevStart:i]) + chunk
				}
				chunks = append(chunks, strings.TrimSpace(chunk))
			}
			continue
		}

		if currentTokens+lineTokens > maxTokens {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
		currentTokens += lineTokens
	}
	flush()

	// Apply overlap
	if overlapTokens > 0 && len(chunks) > 1 {
		for i := 1; i < len(chunks); i++ {
			tail := tailByTokens(chunks[i-1], overlapTokens)
			if tail != "" {
				chunks[i] = tail + "\n" + chunks[i]
			}
		}
	}
	return chunks
}

func tailByTokens(s string, tokens int) string {
	runes := []rune(s)
	if len(runes) <= tokens {
		return s
	}
	return string(runes[len(runes)-tokens:])
}

func approxTokenCount(s string) int {
	return int(math.Ceil(float64(utf8.RuneCountInString(s)) / 1.3))
}

func extractHashtags(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		if strings.HasPrefix(w, "#") {
			out = append(out, strings.TrimPrefix(w, "#"))
		}
	}
	return out
}

func keywordPieces(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, "，。,。!！?？、()（）")
		if utf8.RuneCountInString(w) >= 2 {
			out = append(out, w)
		}
	}
	return out
}
