package flux

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Jeffail/gabs"
	jsonpatch "github.com/evanphx/json-patch"
)

// ChartRequestSignature is the parameter for a chart request
type ChartRequestSignature struct {
	// ticker for the content of a chart request
	Ticker string

	// range is the timeframe for chart data to be received, see specs.txt
	Range string

	// width is the width of the candles to be received, see specs.txt
	Width string
}

// shortname presented as TICKER@RANGE:WIDTH
func (c *ChartRequestSignature) shortName() string {
	return fmt.Sprintf("%s@%s:%s", c.Ticker, c.Range, c.Width)
}

type keyCachedData struct {
	Data cachedData `json:"data"`
}

type cachedData struct {
	Symbol     string `json:"symbol"`
	Instrument struct {
		Symbol                 string `json:"symbol"`
		RootSymbol             string `json:"rootSymbol"`
		DisplaySymbol          string `json:"displaySymbol"`
		RootDisplaySymbol      string `json:"rootDisplaySymbol"`
		FutureOption           bool   `json:"futureOption"`
		Description            string `json:"description"`
		Multiplier             int    `json:"multiplier"`
		SpreadsSupported       bool   `json:"spreadsSupported"`
		Tradeable              bool   `json:"tradeable"`
		InstrumentType         string `json:"instrumentType"`
		ID                     int    `json:"id"`
		SourceType             string `json:"sourceType"`
		IsFutureProduct        bool   `json:"isFutureProduct"`
		HasOptions             bool   `json:"hasOptions"`
		Composite              bool   `json:"composite"`
		FractionalType         string `json:"fractionalType"`
		DaysToExpiration       int    `json:"daysToExpiration"`
		SpreadDaysToExpiration string `json:"spreadDaysToExpiration"`
		Cusip                  string `json:"cusip"`
		Industry               int    `json:"industry"`
		Spc                    int    `json:"spc"`
		ExtoEnabled            bool   `json:"extoEnabled"`
		Flags                  int    `json:"flags"`
	} `json:"instrument"`
	Timestamps []int64   `json:"timestamps"`
	Open       []float64 `json:"open"`
	High       []float64 `json:"high"`
	Low        []float64 `json:"low"`
	Close      []float64 `json:"close"`
	Volume     []int     `json:"volume"`
	Events     []struct {
		Symbol      string `json:"symbol"`
		CompanyName string `json:"companyName"`
		Website     string `json:"website"`
		IsActual    bool   `json:"isActual"`
		Time        int64  `json:"time"`
	} `json:"events"`
	Service    string `json:"service"`
	RequestID  string `json:"requestId"`
	RequestVer int    `json:"requestVer"`
}

type newChartObject struct {
	Op    string     `json:"op"`
	Path  string     `json:"path"`
	Value cachedData `json:"value"`
}

type updateChartObject struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value int    `json:"value"`
}

func (s *Session) chartHandler(msg []byte, gab *gabs.Container) {
	for _, patch := range gab.S("payloadPatches", "0", "patches").Children() {

		// TODO: implement actual error handling, currently using log.Fatal()
		// which is bad
		var err error
		bytesJson, err := patch.MarshalJSON()
		if err != nil {
			log.Fatal(err)
		}

		var modifiedChart []byte

		if patch.S("path").String() == "/error" {
			continue
		}

		if patch.S("path").String() == `""` {
			newChart := newChartObject{}
			json.Unmarshal(bytesJson, &newChart)
			newChart.Path = "/data"
			modifiedChart, err = json.Marshal([]newChartObject{newChart})
		} else {
			updatedChart := updateChartObject{}
			json.Unmarshal(bytesJson, &updatedChart)
			updatedChart.Path = "/data" + updatedChart.Path
			modifiedChart, err = json.Marshal([]updateChartObject{updatedChart})
		}

		if err != nil {
			log.Fatal(err)
		}
		jspatch, err := jsonpatch.DecodePatch(modifiedChart)
		if err != nil {
			log.Fatal(err)
		}

		s.CurrentState, err = jspatch.Apply(s.CurrentState)
		if err != nil {
			log.Fatal(err)
		}

		if patch.S("path").String() == `""` {
			d, _ := s.dataAsChartObject()
			s.TransactionChannel <- *d
		}
	}
}

// RequestChart takes a ChartRequestSignature as an input and responds with a
// cachedData object, it utilizes the cached if it can (with updated diffs), or
// else it makes a new request and waits for it - if a ticker does not load in
// time, ErrNotReceviedInTime is sent as an error
func (s *Session) RequestChart(specs ChartRequestSignature) (*cachedData, error) {

	// force capitalization of tickers, since the socket is case sensitive
	specs.Ticker = strings.ToUpper(specs.Ticker)

	if s.CurrentChartHash == specs.shortName() {
		d, err := s.dataAsChartObject()
		if err != nil {
			return nil, err
		}
		return d, nil
	}

	s.CurrentChartHash = specs.shortName()

	req := gatewayRequest{
		Service:           "chart",
		ID:                "chart",
		Ver:               0,
		Symbol:            specs.Ticker,
		AggregationPeriod: specs.Width,
		Studies:           []string{},
		Range:             specs.Range,
	}
	payload := gatewayRequestLoad{[]gatewayRequest{req}}
	s.wsConn.WriteJSON(payload)

	internalChannel := make(chan cachedData)
	ctx, _ := context.WithTimeout(context.Background(), time.Second)

	go func() {
		for {
			select {

			case recvPayload := <-s.TransactionChannel:
				if strings.ToUpper(recvPayload.Symbol) == strings.ToUpper(specs.Ticker) {
					internalChannel <- recvPayload
				}

			case <-ctx.Done():
				break
			}
		}
	}()

	select {

	case recvPayload := <-internalChannel:
		return &recvPayload, nil

	case <-ctx.Done():
		return nil, ErrNotReceivedInTime
	}

	//unreachable code
	return nil, nil
}
