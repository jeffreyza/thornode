package cli

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Option struct {
	Name    string
	Default bool
}

type Options struct {
	*tview.Box
	app           *tview.Application
	options       []Option
	unselected    map[int]bool
	currentOption int
}

func NewOptions(app *tview.Application, options []Option) *Options {
	o := &Options{
		Box:           tview.NewBox(),
		app:           app,
		options:       options,
		unselected:    make(map[int]bool),
		currentOption: -1,
	}
	for i, option := range options {
		o.unselected[i] = !option.Default
	}
	return o
}

func (o *Options) Selected() map[string]bool {
	selected := make(map[string]bool)
	for i, option := range o.options {
		if !o.unselected[i] {
			selected[option.Name] = true
		}
	}
	return selected
}

func (o *Options) Draw(screen tcell.Screen) {
	o.DrawForSubclass(screen, o)
	x, y, width, _ := o.GetInnerRect()

	// continue button, green background on selected
	continueText := " Continue "
	button := tview.NewBox()
	button.SetRect(x, y, len(continueText), 1)
	if o.currentOption == -1 {
		button.SetBackgroundColor(tcell.ColorGreen)
		button.Draw(screen)
	} else {
		button.SetBackgroundColor(tcell.ColorGray)
		button.Draw(screen)
	}
	tview.Print(screen, continueText, x, y, width, tview.AlignLeft, tcell.ColorBlack)

	for i, option := range o.options {
		checkBox := "\u25c9" // checked
		if o.unselected[i] {
			checkBox = "\u25ef" // unchecked
		}
		line := fmt.Sprintf(` %s %s`, checkBox, option.Name)
		if i == o.currentOption {
			tview.Print(screen, line, x, y+i+1, width, tview.AlignLeft, tcell.ColorGreen)
		} else {
			tview.Print(screen, line, x, y+i+1, width, tview.AlignLeft, tcell.ColorWhite)
		}
	}
}

func (o *Options) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return o.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		switch event.Key() {
		case tcell.KeyUp, tcell.KeyCtrlK:
			o.currentOption--
			if o.currentOption < -1 {
				o.currentOption = -1
			}
		case tcell.KeyDown, tcell.KeyCtrlJ:
			o.currentOption++
			if o.currentOption >= len(o.options) {
				o.currentOption = len(o.options) - 1
			}
		case tcell.KeyEnter:
			// handle continue
			if o.currentOption == -1 {
				o.app.Stop()
			}
			o.unselected[o.currentOption] = !o.unselected[o.currentOption]
		}
	})
}
