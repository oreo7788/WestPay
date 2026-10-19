package main

type PaymentOrder struct {
	PayURL        string  `json:"payUrl"`
	QRImage       string  `json:"qrImage"`
	QRImageURL    string  `json:"qrImageUrl"`
	ExpiresAt     int64   `json:"expiresAt"`
	CanPay        bool    `json:"canPay"`
	Description   string  `json:"description"`
	Plugin        string  `json:"plugin"`
	ProviderOrder string  `json:"providerOrder"`
	SystemID      string  `json:"systemId"`
	RechargeID    string  `json:"rechargeId"`
	MerchantOrder string  `json:"merchantOrder"`
	MerchantID    string  `json:"merchantId"`
	MerchantName  string  `json:"merchantName"`
	ProductCode   string  `json:"productCode"`
	ProductName   string  `json:"productName"`
	Amount        float64 `json:"amount"`
	ChannelName   string  `json:"channelName"`
	ChannelID     string  `json:"channelId"`
	Method        string  `json:"method"`
	CreatedAt     string  `json:"createdAt"`
	CallbackAt    string  `json:"callbackAt"`
	Status        string  `json:"status"`
	ProviderTxn   string  `json:"providerTxn"`
	QueryCount    int     `json:"queryCount"`
}

type Merchant struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	TodayCount  int     `json:"todayCount"`
	TodayRate   float64 `json:"todayRate"`
	Yesterday   int     `json:"yesterdayCount"`
	Recent30m   int     `json:"recent30m"`
	Recent10m   int     `json:"recent10m"`
	Prepaid     float64 `json:"prepaid"`
	Remaining   float64 `json:"remaining"`
	TotalPrepay float64 `json:"totalPrepay"`
	Enabled     bool    `json:"enabled"`
}

type Product struct {
	Method  string  `json:"method"`
	Code    string  `json:"code"`
	Name    string  `json:"name"`
	FeeRate float64 `json:"feeRate"`
	Enabled bool    `json:"enabled"`
}

type Channel struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	FeeRate     float64 `json:"feeRate"`
	MinAmount   float64 `json:"minAmount"`
	MaxAmount   float64 `json:"maxAmount"`
	Method      string  `json:"method"`
	Plugin      string  `json:"plugin"`
	TodayAmount float64 `json:"todayAmount"`
	Yesterday   float64 `json:"yesterdayAmount"`
	Enabled     bool    `json:"enabled"`
}

type FinanceRow struct {
	Name      string  `json:"name"`
	Orders    int     `json:"orders"`
	Amount    float64 `json:"amount"`
	Fee       float64 `json:"fee"`
	Prepaid   float64 `json:"prepaid"`
	Remaining float64 `json:"remaining"`
	Date      string  `json:"date"`
	Type      string  `json:"type"`
	Before    float64 `json:"before"`
	After     float64 `json:"after"`
	RelatedID string  `json:"relatedId"`
}

type Dashboard struct {
	YesterdayAmount float64   `json:"yesterdayAmount"`
	TodayOrders     int       `json:"todayOrders"`
	TodaySuccess    int       `json:"todaySuccess"`
	TodayAmount     float64   `json:"todayAmount"`
	SuccessRate     float64   `json:"successRate"`
	TodayProfit     float64   `json:"todayProfit"`
	MonthAmount     float64   `json:"monthAmount"`
	Yesterday       int       `json:"yesterdayOrders"`
	Trend           []float64 `json:"trend"`
}
