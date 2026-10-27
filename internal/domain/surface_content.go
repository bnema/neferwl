package domain

// ContentType is the committed wp_content_type_v1 hint.
type ContentType uint8

const (
	ContentNone ContentType = iota
	ContentPhoto
	ContentVideo
	ContentGame
)
