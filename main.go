package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"math"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/anthonynsimon/bild/adjust"
	"github.com/anthonynsimon/bild/effect"
	"github.com/anthonynsimon/bild/imgio"
	"github.com/anthonynsimon/bild/transform"
)

const (
	version    = "0.1.0-dev"
	jpegPrefix = "data:image/jpeg;base64,"
	pngPrefix  = "data:image/png;base64,"
)

func main() {
	println("starting go wasm")
	app := NewApp()
	jsa := NewJsApp(*app)
	select {
	case <-jsa.done:
		jsa.Release()
	}
	println("ending go wasm")
}

type effectFn func(image.Image) image.Image

type Effect struct {
	Name     string
	Min, Max int
}

func (eff *Effect) GetEffectFn(values ...float64) effectFn {
	switch eff.Name {
	case "brightness":
		return func(img image.Image) image.Image { return adjust.Brightness(img, values[0]) }
	case "contrast":
		return func(img image.Image) image.Image { return adjust.Contrast(img, values[0]) }
	case "edge-detection":
		return func(img image.Image) image.Image { return effect.EdgeDetection(img, values[0]) }

	default:
		log("effect not found: ", eff.Name)
		return nil
	}
}

var transformationTmpl = `<div><label for="{{ .Name }}">{{ .Name }}</label><input type="range" min="{{ .Min }}" max="{{ .Max}}" value="0" step="0.1" id="{{ .Id }}"></div>`

type transformFn func(values ...float64) effectFn

type Transformation struct {
	Effect
	Id     string
	Values []float64
	Fn     transformFn
}

func (t *Transformation) Transform() effectFn {

	return t.Fn(t.Values...)
}

func (t *Transformation) Render() string {
	var rendered strings.Builder
	tmpl, err := template.New(t.Name).Parse(transformationTmpl)
	if err != nil {
		log(err)
	}
	err = tmpl.Execute(&rendered, t)
	if err != nil {
		fmt.Println(err)
	}
	return rendered.String()
}

type App struct {
	buf        bytes.Buffer
	cnt        int
	dstWidth   int
	sourceImg  image.Image
	resizedImg image.Image

	Effects []Effect

	transformations []Transformation
}

func NewApp() *App {
	return &App{
		transformations: make([]Transformation, 0),
		Effects: []Effect{
			Effect{
				Name: "contrast",
				Min:  -2,
				Max:  2,
			},
			Effect{
				Name: "brightness",
				Min:  -2,
				Max:  2,
			},
			Effect{
				Name: "edge-detection",
				Min:  -2,
				Max:  2,
			},
		},
		cnt:      0,
		dstWidth: 200,
	}
}

var appTmpl = `
      <div id="uploader">
        <input type="file" value="" name="uploader" id="uploader"/>
      </div>
      <div class="separator">preview:</div>
        <div>
                <image id="previewImg" class="image" />
                <image id="targetImg" class="image" />
        </div>

      <div class="separator">Select an effect:</div>
      <select name="effect" id="effectSelector">
	  {{ range .Effects }}<option name="{{ .Name }}" id="{{ .Name }}">{{ .Name }}</option>{{ end }}
      </select>
      <button id="addEffectBtn">Add</button>
      <div id="effects">
      </div>
`

func (app *App) Render() string {
	var rendered strings.Builder
	tmpl, err := template.New("app").Parse(appTmpl)
	if err != nil {
		// log(err)
		fmt.Println(err)
	}
	err = tmpl.Execute(&rendered, app)
	if err != nil {
		fmt.Println(err)
	}
	return rendered.String()
}

func (app *App) Append(t Transformation) {
	app.transformations = append(app.transformations, t)
	log("lenght of app.transformations", len(app.transformations))
}

func (app *App) Update(Id string, values ...float64) {
	for i, t := range app.transformations {
		if t.Id == Id {
			t.Values = values
			app.transformations[i] = t
			break
		}
	}
}

func (app *App) NewSourceImgFromString(simg string) {
	switch {
	case strings.HasPrefix(simg, jpegPrefix):
		simg = strings.TrimPrefix(simg, jpegPrefix)
	case strings.HasPrefix(simg, pngPrefix):
		simg = strings.TrimPrefix(simg, pngPrefix)
	default:
		log("unrecognized image format")
		return
	}

	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(simg))
	var err error
	app.sourceImg, _, err = image.Decode(reader)
	if err != nil {
		log(err.Error())
		return
	}
	srcWidth, srcHeight := app.sourceImg.Bounds().Dx(), app.sourceImg.Bounds().Dy()
	dstWidth := app.dstWidth
	ratio := float64(srcHeight) / float64(srcWidth)
	dstHeight := int(math.Ceil(ratio * float64(dstWidth)))
	app.resizedImg = transform.Resize(app.sourceImg, dstWidth, dstHeight, transform.Linear)

}

func (app *App) PreviewImg() image.Image {
	img := app.resizedImg
	for _, t := range app.transformations {
		log(t.Id)
		img = t.Transform()(img)
	}
	return img
}

type JsApp struct {
	App
	done chan struct{}

	ShutdownCallback      js.Callback
	UploadCallback        js.Callback
	AddEffectCallback     js.Callback
	ChangeEffectsCallback js.Callback

	buf bytes.Buffer
}

func NewJsApp(app App) *JsApp {
	jsa := &JsApp{
		App:  app,
		done: make(chan struct{}),
	}

	getElementById("app").Call("insertAdjacentHTML", "beforeend", jsa.App.Render())

	jsa.ShutdownCallback = js.NewEventCallback(js.StopPropagation, func(ev js.Value) {
		log("event", ev)
		ev.Get("target").Set("disabled", true)
		getElementById("status").Set("innerText", "go wasm app is closed")
		getElementById("app").Set("innerHTML", "")
		jsa.done <- struct{}{}
	})
	getElementById("shutdownBtn").Call("addEventListener", "click", jsa.ShutdownCallback)

	jsa.UploadCallback = js.NewEventCallback(js.PreventDefault, func(ev js.Value) {
		log("event", ev)
		file := ev.Get("target").Get("files").Get("0")
		freader := js.Global().Get("FileReader").New()
		freader.Set("onload", js.NewEventCallback(js.PreventDefault, func(ev js.Value) {
			jsa.NewSourceImgFromString(ev.Get("target").Get("result").String())
			jsa.UpdateImgSrcById("previewImg", jsa.resizedImg)
			jsa.UpdateImgSrcById("targetImg", jsa.PreviewImg())
		}))
		freader.Call("readAsDataURL", file)
	})
	getElementById("uploader").Call("addEventListener", "change", jsa.UploadCallback)

	jsa.AddEffectCallback = js.NewEventCallback(js.StopPropagation, func(ev js.Value) {
		log("event", ev)
		jsa.cnt++
		effectSelector := getElementById("effectSelector")
		effectSelected := effectSelector.Get("options").Call("item", effectSelector.Get("selectedIndex"))
		log(effectSelected)
		eff := Effect{
			Name: effectSelected.Get("id").String(),
			Min:  -2,
			Max:  2,
		}
		t := Transformation{
			Effect: eff,
			Id:     effectSelected.Get("id").String() + strconv.Itoa(jsa.cnt),
			Values: []float64{0}, // default value
			Fn:     eff.GetEffectFn,
		}
		jsa.Append(t)
		getElementById("effects").Call("insertAdjacentHTML", "beforeend", t.Render())
	})
	getElementById("addEffectBtn").Call("addEventListener", "click", jsa.AddEffectCallback)

	jsa.ChangeEffectsCallback = js.NewEventCallback(js.PreventDefault, func(ev js.Value) {
		log("event", ev)
		jsa.Update(ev.Get("target").Get("id").String(), ev.Get("target").Get("valueAsNumber").Float())
		jsa.UpdateImgSrcById("targetImg", jsa.PreviewImg())
	})
	getElementById("effects").Call("addEventListener", "change", jsa.ChangeEffectsCallback)

	return jsa
}

func (jsa *JsApp) Release() {
	// In tip callback.Close() is renamed callback.Release()
	jsa.ShutdownCallback.Close()
	jsa.UploadCallback.Close()
	jsa.AddEffectCallback.Close()
	jsa.ChangeEffectsCallback.Close()
}

func (jsa *JsApp) UpdateImgSrcById(Id string, img image.Image) {
	if img == nil {
		return
	}
	enc := imgio.JPEGEncoder(90)
	err := enc(&jsa.buf, img)
	if err != nil {
		log(err.Error())
	}
	// setting the previewImg src property
	getElementById(Id).
		Set("src", jpegPrefix+base64.StdEncoding.EncodeToString(jsa.buf.Bytes()))
	jsa.buf.Reset()
}

func getElementById(Id string) js.Value {
	return js.Global().Get("document").Call("getElementById", Id)
}

func log(args ...interface{}) {
	js.Global().Get("console").Call("log", args...)
}
