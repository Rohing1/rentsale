// Package webassets exposes the management UI files bundled into the server binary.
package webassets

import "embed"

// Files contains the complete browser UI required by the application.
// Keeping the asset list explicit makes an accidental large-file embed visible in review.
//
//go:embed index.html styles.css app.js
var Files embed.FS
