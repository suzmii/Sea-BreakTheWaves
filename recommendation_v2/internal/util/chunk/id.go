package chunk

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// NormalizeArticleID 生成或规范化文章 ID。
func NormalizeArticleID(currentID string, a Article) string {
	if currentID != "" && !strings.HasPrefix(currentID, "art_") {
		return currentID
	}

	seed := a.Title + a.Cover + fmt.Sprintf("%v", a.TypeTags) +
		strings.Join(sectionH2s(a), "|") + sectionTexts(a) + sectionImageURLs(a)

	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("article:"+seed))
	return "art_" + strings.ReplaceAll(id.String(), "-", "")[:26]
}

// BuildChunkID 根据文章 ID 和序号+内容生成 chunk ID。
func BuildChunkID(articleID string, seq int, h2, content string) string {
	id := uuid.NewSHA1(uuid.NameSpaceURL,
		[]byte(fmt.Sprintf("chunk:%s|%d|%s|%s", articleID, seq, h2, content)))
	return "chk_" + strings.ReplaceAll(id.String(), "-", "")[:26]
}

func sectionH2s(a Article) []string {
	out := make([]string, len(a.Sections))
	for i, s := range a.Sections {
		out[i] = s.H2
	}
	return out
}

func sectionTexts(a Article) string {
	var b strings.Builder
	for _, s := range a.Sections {
		for _, blk := range s.Blocks {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
	}
	return b.String()
}

func sectionImageURLs(a Article) string {
	var b strings.Builder
	for _, s := range a.Sections {
		for _, blk := range s.Blocks {
			if blk.Type == "image" {
				b.WriteString(blk.ImageURL)
			}
		}
	}
	return b.String()
}
