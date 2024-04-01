package static

import (
	"bytes"
	_ "embed"
	"github.com/pkg/errors"
	"text/template"
)

//go:embed swagger-ui.html.template
var swaggerUIHtmlTemplateContent string

func GenerateFullHtml(url string) (string, error) {
	t, err := template.New("swagger-ui.html").Parse(swaggerUIHtmlTemplateContent)
	if err != nil {
		panic(err)
	}
	var buffer = bytes.NewBuffer(nil)
	type templateData struct {
		Url string
	}
	if err := t.Execute(buffer, &templateData{
		Url: url,
	}); err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	var fullHtml = buffer.String()
	return fullHtml, nil
}
