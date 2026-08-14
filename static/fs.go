package static

import "embed"

// Content holds the embedded admin UI assets.
//
//go:embed index.html css/*.css js/*.js assets/*
var Content embed.FS
