package chunk

// Article 待入库的文章。
type Article struct {
	ArticleID string   `json:"article_id"`
	Title     string   `json:"title"`
	Cover     string   `json:"cover"`
	TypeTags  []string `json:"type_tags"`
	Tags      []string `json:"tags"`
	Score     float64  `json:"score"`
	Author    string   `json:"author"`
	GeoCity   string   `json:"geo_city"`
	Sections  []Section `json:"sections"`
}

// Section 文章的一个章节（## 标题下的内容）。
type Section struct {
	H2     string  `json:"h2"`
	Blocks []Block `json:"blocks"`
}

// Block 段落级别的内容单元。
type Block struct {
	Type     string `json:"type"`     // "text" / "image"
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

// Chunk 切分后的文本片段。
type Chunk struct {
	ChunkID     string   `json:"chunk_id"`
	ArticleID   string   `json:"article_id"`
	H2          string   `json:"h2"`
	Content     string   `json:"content"`
	Tokens      int      `json:"tokens"`
	ContentType string   `json:"content_type"` // "text"
	ImageURLs   []string `json:"image_urls"`
}

// SplitResult 切分结果。
type SplitResult struct {
	CoarseText  string   `json:"coarse_text"`
	CoarseIntro string   `json:"coarse_intro"`
	FineChunks  []Chunk  `json:"fine_chunks"`
	Keywords    []string `json:"keywords"`
	KeywordScore float32 `json:"keyword_score"`
}
