package main

import (
	"errors"
	"fmt"
	"time"

	"tradebin-mm/app"

	"github.com/sirupsen/logrus"
)

const (
	restartInterval = 30
)

type marketFiller interface {
	FillOrderBook() error
}

type volumeMaker interface {
	MakeVolume() error
}

type App struct {
	l logrus.FieldLogger
	v volumeMaker
	f marketFiller
}

func NewApp(l logrus.FieldLogger, v volumeMaker, f marketFiller) (*App, error) {
	if l == nil || v == nil || f == nil {
		return nil, fmt.Errorf("logger, volume maker and market filler must be provided")
	}

	return &App{
		l: l.WithField("service", "APP"),
		v: v,
		f: f,
	}, nil
}

func (a *App) Start() {
	a.l.Info("starting app...")
	a.execute()
}

func (a *App) execute() {
	defer a.afterExecute()
	a.l.Debug("executing app...")

	err := a.f.FillOrderBook()
	if errors.Is(err, app.ErrMarketHalted) {
		// the filler already warned and cancelled; only log what went wrong besides the halt
		if err != app.ErrMarketHalted {
			a.l.WithError(err).Error("error encountered while exiting halted market")
		}

		return
	}

	if err != nil {
		a.l.WithError(err).Errorf("error encountered while filling order book")

		return
	}

	err = a.v.MakeVolume()
	if err != nil {
		a.l.WithError(err).Errorf("error encountered while making volume")
	}
}

func (a *App) afterExecute() {
	a.l.Info("app finished")
	time.AfterFunc(time.Second*restartInterval, func() {
		a.execute()
	})
	a.l.Infof("restarting in %d seconds", restartInterval)
}
