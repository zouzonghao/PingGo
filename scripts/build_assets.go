package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"

	"github.com/tdewolff/minify/v2/js"
	"github.com/tdewolff/minify/v2/json"
	"github.com/tdewolff/minify/v2/svg"
)

func main() {
	m := minify.New()
	m.AddFunc("text/css", css.Minify)

	m.AddFunc("image/svg+xml", svg.Minify)
	m.AddFuncRegexp(regexp.MustCompile("^(application|text)/(x-)?(java|ecma)script$"), js.Minify)
	m.AddFuncRegexp(regexp.MustCompile("[/+]json$"), json.Minify)

	root := "static"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		var mimetype string
		switch ext {
		case ".css":
			mimetype = "text/css"
		case ".js":
			mimetype = "text/javascript"

		case ".json":
			mimetype = "application/json"
		case ".svg":
			mimetype = "image/svg+xml"
		default:
			return nil
		}

		log.Printf("Minifying %s...", path)
		input, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		output, err := m.Bytes(mimetype, input)
		if err != nil {
			return err
		}

		if os.Getenv("PRODUCTION_BUILD") != "true" {
			log.Printf("Dry run: Skipping write to %s (set PRODUCTION_BUILD=true to overwrite)", path)
			return nil
		}

		return os.WriteFile(path, output, 0644)
	})

	if err != nil {
		log.Fatal(err)
	}
	log.Println("Asset minification complete.")
}
