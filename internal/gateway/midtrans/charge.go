package midtrans

import (
	"math"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// Wire types for /v2/charge, limited to the fields the hub uses.

type chargeRequest struct {
	PaymentType        string             `json:"payment_type"`
	TransactionDetails transactionDetails `json:"transaction_details"`
	CustomerDetails    *customerDetails   `json:"customer_details,omitempty"`
	CustomExpiry       *customExpiry      `json:"custom_expiry,omitempty"`
	BankTransfer       *bankTransfer      `json:"bank_transfer,omitempty"`
	QRIS               *qrisOptions       `json:"qris,omitempty"`
	GoPay              *gopayOptions      `json:"gopay,omitempty"`
	ShopeePay          *shopeePayOptions  `json:"shopeepay,omitempty"`
	CStore             *convenienceStore  `json:"cstore,omitempty"`
}

type transactionDetails struct {
	OrderID     string `json:"order_id"`
	GrossAmount int64  `json:"gross_amount"`
}

type customerDetails struct {
	FirstName string `json:"first_name,omitempty"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
}

type customExpiry struct {
	OrderTime      string `json:"order_time"`
	ExpiryDuration int    `json:"expiry_duration"`
	Unit           string `json:"unit"`
}

type bankTransfer struct {
	Bank string `json:"bank"`
}

type qrisOptions struct {
	Acquirer string `json:"acquirer"`
}

type gopayOptions struct {
	EnableCallback bool   `json:"enable_callback,omitempty"`
	CallbackURL    string `json:"callback_url,omitempty"`
}

type shopeePayOptions struct {
	CallbackURL string `json:"callback_url,omitempty"`
}

type convenienceStore struct {
	Store   string `json:"store"`
	Message string `json:"message,omitempty"`
}

type statusOnly struct {
	StatusCode    string `json:"status_code"`
	StatusMessage string `json:"status_message"`
}

type chargeResponse struct {
	statusOnly
	TransactionID     string     `json:"transaction_id"`
	OrderID           string     `json:"order_id"`
	TransactionStatus string     `json:"transaction_status"`
	FraudStatus       string     `json:"fraud_status"`
	VANumbers         []vaNumber `json:"va_numbers"`
	PermataVANumber   string     `json:"permata_va_number"`
	PaymentCode       string     `json:"payment_code"`
	QRString          string     `json:"qr_string"`
	Actions           []action   `json:"actions"`
	ExpiryTime        string     `json:"expiry_time"`
	SettlementTime    string     `json:"settlement_time"`
}

type vaNumber struct {
	Bank     string `json:"bank"`
	VANumber string `json:"va_number"`
}

type action struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type refundRequest struct {
	RefundKey string `json:"refund_key"`
	Amount    int64  `json:"amount"`
	Reason    string `json:"reason,omitempty"`
}

type refundResponse struct {
	statusOnly
	RefundKey string `json:"refund_key"`
}

var vaBanks = map[domain.Channel]string{
	domain.ChannelBCA:     "bca",
	domain.ChannelBNI:     "bni",
	domain.ChannelBRI:     "bri",
	domain.ChannelCIMB:    "cimb",
	domain.ChannelPermata: "permata",
}

func (g *Gateway) buildCharge(req gateway.CreatePaymentRequest) (*chargeRequest, error) {
	c := &chargeRequest{
		TransactionDetails: transactionDetails{OrderID: req.PaymentID, GrossAmount: req.Amount},
	}
	if req.Customer != (domain.Customer{}) {
		c.CustomerDetails = &customerDetails{FirstName: req.Customer.Name, Email: req.Customer.Email, Phone: req.Customer.Phone}
	}
	if !req.ExpiresAt.IsZero() {
		now := g.now()
		c.CustomExpiry = &customExpiry{
			OrderTime:      now.In(gateway.WIB).Format(timeLayout + " -0700"),
			ExpiryDuration: int(math.Ceil(req.ExpiresAt.Sub(now).Minutes())),
			Unit:           "minute",
		}
	}

	unsupported := gateway.UnsupportedChannel(domain.ProviderMidtrans, req.Method, req.Channel)
	switch req.Method {
	case domain.MethodVirtualAccount:
		bank, ok := vaBanks[req.Channel]
		if !ok {
			return nil, unsupported
		}
		c.PaymentType = "bank_transfer"
		c.BankTransfer = &bankTransfer{Bank: bank}

	case domain.MethodQRIS:
		c.PaymentType = "qris"
		c.QRIS = &qrisOptions{Acquirer: "gopay"}

	case domain.MethodEWallet:
		switch req.Channel {
		case domain.ChannelGoPay:
			c.PaymentType = "gopay"
			c.GoPay = &gopayOptions{EnableCallback: req.ReturnURL != "", CallbackURL: req.ReturnURL}
		case domain.ChannelShopeePay:
			c.PaymentType = "shopeepay"
			c.ShopeePay = &shopeePayOptions{CallbackURL: req.ReturnURL}
		default:
			return nil, unsupported
		}

	case domain.MethodRetailOutlet:
		if req.Channel != domain.ChannelAlfamart && req.Channel != domain.ChannelIndomaret {
			return nil, unsupported
		}
		c.PaymentType = "cstore"
		c.CStore = &convenienceStore{Store: string(req.Channel), Message: req.Description}

	default: // cards need client-side tokenization (Snap/JS)
		return nil, unsupported
	}
	return c, nil
}

func (r chargeResponse) action() domain.PaymentAction {
	a := domain.PaymentAction{PaymentCode: r.PaymentCode, QRString: r.QRString}
	if len(r.VANumbers) > 0 {
		a.VirtualAccountNumber = r.VANumbers[0].VANumber
	}
	if r.PermataVANumber != "" {
		a.VirtualAccountNumber = r.PermataVANumber
	}
	for _, act := range r.Actions {
		switch act.Name {
		case "generate-qr-code":
			a.QRImageURL = act.URL
		case "deeplink-redirect":
			a.DeepLinkURL = act.URL
		}
	}
	return a
}
