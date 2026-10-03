package gridcore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type MarketMeta struct {
	Symbol                 string `json:"symbol"`
	MarketID               int16  `json:"market_id"`
	MarketType             string `json:"market_type"`
	Status                 string `json:"status"`
	MinBaseAmount          string `json:"min_base_amount"`
	SupportedSizeDecimals  uint8  `json:"supported_size_decimals"`
	SupportedPriceDecimals uint8  `json:"supported_price_decimals"`
	PriceDecimals          uint8  `json:"price_decimals"`
	SizeDecimals           uint8  `json:"size_decimals"`
	QuoteMultiplier        int64  `json:"quote_multiplier"`
	MarkPrice              string `json:"mark_price"`
}

type OrderBookDetailsResponse struct {
	Code                 int          `json:"code"`
	Message              string       `json:"message"`
	OrderBookDetails     []MarketMeta `json:"order_book_details"`
	SpotOrderBookDetails []struct{}   `json:"spot_order_book_details"`
}

type AccountResponse struct {
	Code             int               `json:"code"`
	Message          string            `json:"message"`
	Index            int64             `json:"index"`
	L1Address        string            `json:"l1_address"`
	AvailableBalance string            `json:"available_balance"`
	Collateral       string            `json:"collateral"`
	Positions        []AccountPosition `json:"positions"`
}

type DetailedAccountsResponse struct {
	Code       int               `json:"code"`
	Message    string            `json:"message"`
	Total      int64             `json:"total"`
	Accounts   []DetailedAccount `json:"accounts"`
	NextCursor string            `json:"next_cursor"`
}

type DetailedAccount struct {
	Index            int64             `json:"index"`
	L1Address        string            `json:"l1_address"`
	AvailableBalance string            `json:"available_balance"`
	Collateral       string            `json:"collateral"`
	Positions        []AccountPosition `json:"positions"`
}

type AccountPosition struct {
	MarketID int16  `json:"market_id"`
	Symbol   string `json:"symbol"`
	Sign     int    `json:"sign"`
	Position string `json:"position"`
}

type AccountOrdersResponse struct {
	Code       int     `json:"code"`
	Message    string  `json:"message"`
	NextCursor string  `json:"next_cursor"`
	Orders     []Order `json:"orders"`
}

type Order struct {
	OrderIndex          int64  `json:"order_index"`
	ClientOrderIndex    int64  `json:"client_order_index"`
	MarketIndex         int16  `json:"market_index"`
	MarketID            int16  `json:"market_id"`
	InitialBaseAmount   string `json:"initial_base_amount"`
	Price               string `json:"price"`
	RemainingBaseAmount string `json:"remaining_base_amount"`
	IsAsk               bool   `json:"is_ask"`
	Type                string `json:"type"`
	TimeInForce         string `json:"time_in_force"`
	ReduceOnly          bool   `json:"reduce_only"`
	Status              string `json:"status"`
	FilledBaseAmount    string `json:"filled_base_amount"`
}

type AccountMarketUpdate struct {
	Channel  string       `json:"channel"`
	Account  int64        `json:"account"`
	Orders   []Order      `json:"orders"`
	Trades   []Trade      `json:"trades"`
	Position PositionList `json:"position"`
	Type     string       `json:"type"`
}

type PositionList []AccountPosition

func (p *PositionList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || len(data) == 0 {
		*p = nil
		return nil
	}
	if data[0] == '[' {
		var positions []AccountPosition
		if err := json.Unmarshal(data, &positions); err != nil {
			return err
		}
		*p = positions
		return nil
	}
	if data[0] == '{' {
		var position AccountPosition
		if err := json.Unmarshal(data, &position); err != nil {
			return err
		}
		*p = []AccountPosition{position}
		return nil
	}
	return fmt.Errorf("position payload must be an object, array, or null")
}

type Trade struct {
	TradeID      int64  `json:"trade_id"`
	TradeIDStr   string `json:"trade_id_str"`
	TxHash       string `json:"tx_hash"`
	MarketID     int16  `json:"market_id"`
	Size         string `json:"size"`
	Price        string `json:"price"`
	AskClientID  int64  `json:"ask_client_id"`
	BidClientID  int64  `json:"bid_client_id"`
	AskAccountID int64  `json:"ask_account_id"`
	BidAccountID int64  `json:"bid_account_id"`
	Timestamp    int64  `json:"timestamp"`
}

type SendTxResponse struct {
	Code                     int    `json:"code"`
	Message                  string `json:"message"`
	TxHash                   string `json:"tx_hash"`
	PredictedExecutionTimeMS int64  `json:"predicted_execution_time_ms"`
	VolumeQuotaRemaining     int64  `json:"volume_quota_remaining"`
}

type GridLevel struct {
	Index            int
	BuyPrice         float64
	TPPrice          float64
	BuyClientOrderID int64
	TPClientOrderID  int64
}

type TrackedOrder struct {
	ClientOrderID int64
	MarketID      int16
	Side          string
	Price         float64
	SizeAtomic    int64
	LevelIndex    int
	TPPrice       float64
	BuyPrice      float64
	PriceDecimals uint8
	SizeDecimals  uint8
	Processing    bool
	PlacedAt      time.Time
}
