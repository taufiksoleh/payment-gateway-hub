package doku

import (
	"math"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// Wire types for DOKU Checkout, limited to the fields the hub uses.

type checkoutRequest struct {
	Order    order     `json:"order"`
	Payment  payment   `json:"payment"`
	Customer *customer `json:"customer,omitempty"`
}

type order struct {
	Amount        int64  `json:"amount"`
	InvoiceNumber string `json:"invoice_number"`
	Currency      string `json:"currency"`
	CallbackURL   string `json:"callback_url,omitempty"`
}

type payment struct {
	PaymentDueDate     int      `json:"payment_due_date,omitempty"` // minutes
	PaymentMethodTypes []string `json:"payment_method_types"`
}

type customer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

type checkoutResponse struct {
	Response struct {
		Payment struct {
			TokenID     string `json:"token_id"`
			URL         string `json:"url"`
			ExpiredDate string `json:"expired_date"`
		} `json:"payment"`
	} `json:"response"`
}

type statusResponse struct {
	Transaction struct {
		Status string `json:"status"`
		Date   string `json:"date"`
	} `json:"transaction"`
}

var methodTypes = map[domain.Method]map[domain.Channel]string{
	domain.MethodVirtualAccount: {
		domain.ChannelBCA:     "VIRTUAL_ACCOUNT_BCA",
		domain.ChannelMandiri: "VIRTUAL_ACCOUNT_BANK_MANDIRI",
		domain.ChannelBRI:     "VIRTUAL_ACCOUNT_BRI",
		domain.ChannelBNI:     "VIRTUAL_ACCOUNT_BNI",
		domain.ChannelPermata: "VIRTUAL_ACCOUNT_BANK_PERMATA",
		domain.ChannelCIMB:    "VIRTUAL_ACCOUNT_BANK_CIMB",
	},
	domain.MethodQRIS: {
		domain.ChannelQRIS: "QRIS",
	},
	domain.MethodEWallet: {
		domain.ChannelOVO:       "EMONEY_OVO",
		domain.ChannelShopeePay: "EMONEY_SHOPEE_PAY",
		domain.ChannelDANA:      "EMONEY_DANA",
		domain.ChannelLinkAja:   "EMONEY_LINKAJA",
	},
	domain.MethodRetailOutlet: {
		domain.ChannelAlfamart:  "ONLINE_TO_OFFLINE_ALFA",
		domain.ChannelIndomaret: "ONLINE_TO_OFFLINE_INDOMARET",
	},
	domain.MethodCard: {
		domain.ChannelCard: "CREDIT_CARD",
	},
}

func (g *Gateway) buildCheckout(req gateway.CreatePaymentRequest) (*checkoutRequest, error) {
	methodType, ok := methodTypes[req.Method][req.Channel]
	if !ok {
		return nil, gateway.UnsupportedChannel(domain.ProviderDOKU, req.Method, req.Channel)
	}

	c := &checkoutRequest{
		Order: order{
			Amount:        req.Amount,
			InvoiceNumber: req.PaymentID,
			Currency:      req.Currency,
			CallbackURL:   req.ReturnURL,
		},
		Payment: payment{PaymentMethodTypes: []string{methodType}},
	}
	if !req.ExpiresAt.IsZero() {
		c.Payment.PaymentDueDate = int(math.Ceil(req.ExpiresAt.Sub(g.now()).Minutes()))
	}
	if req.Customer != (domain.Customer{}) {
		c.Customer = &customer{Name: req.Customer.Name, Email: req.Customer.Email, Phone: req.Customer.Phone}
	}
	return c, nil
}
