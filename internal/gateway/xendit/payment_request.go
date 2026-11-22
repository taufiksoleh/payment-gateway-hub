package xendit

import (
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

// Wire types for /payment_requests, limited to the fields the hub uses.

type paymentRequest struct {
	ReferenceID   string        `json:"reference_id"`
	Amount        int64         `json:"amount"`
	Currency      string        `json:"currency"`
	Country       string        `json:"country"`
	Description   string        `json:"description,omitempty"`
	PaymentMethod paymentMethod `json:"payment_method"`
}

type paymentMethod struct {
	Type           string          `json:"type"`
	Reusability    string          `json:"reusability"`
	ReferenceID    string          `json:"reference_id,omitempty"`
	VirtualAccount *channelDetails `json:"virtual_account,omitempty"`
	QRCode         *channelDetails `json:"qr_code,omitempty"`
	EWallet        *channelDetails `json:"ewallet,omitempty"`
	OverTheCounter *channelDetails `json:"over_the_counter,omitempty"`
}

type channelDetails struct {
	ChannelCode       string            `json:"channel_code,omitempty"`
	ChannelProperties channelProperties `json:"channel_properties"`
}

type channelProperties struct {
	// Request fields
	CustomerName     string `json:"customer_name,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	MobileNumber     string `json:"mobile_number,omitempty"`
	SuccessReturnURL string `json:"success_return_url,omitempty"`
	FailureReturnURL string `json:"failure_return_url,omitempty"`

	// Response fields
	VirtualAccountNumber string `json:"virtual_account_number,omitempty"`
	QRString             string `json:"qr_string,omitempty"`
	PaymentCode          string `json:"payment_code,omitempty"`
}

type paymentRequestResponse struct {
	ID            string        `json:"id"`
	ReferenceID   string        `json:"reference_id"`
	Status        string        `json:"status"`
	FailureCode   string        `json:"failure_code"`
	Actions       []action      `json:"actions"`
	PaymentMethod paymentMethod `json:"payment_method"`
}

type action struct {
	Action  string `json:"action"`
	URLType string `json:"url_type"`
	URL     string `json:"url"`
}

type refundRequest struct {
	ReferenceID      string            `json:"reference_id"`
	PaymentRequestID string            `json:"payment_request_id"`
	Amount           int64             `json:"amount"`
	Currency         string            `json:"currency"`
	Reason           string            `json:"reason"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type refundResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type errorResponse struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

var vaChannels = map[domain.Channel]string{
	domain.ChannelBCA:     "BCA",
	domain.ChannelBNI:     "BNI",
	domain.ChannelBRI:     "BRI",
	domain.ChannelMandiri: "MANDIRI",
	domain.ChannelPermata: "PERMATA",
	domain.ChannelBSI:     "BSI",
	domain.ChannelCIMB:    "CIMB",
}

var eWalletChannels = map[domain.Channel]string{
	domain.ChannelOVO:       "OVO",
	domain.ChannelDANA:      "DANA",
	domain.ChannelShopeePay: "SHOPEEPAY",
	domain.ChannelLinkAja:   "LINKAJA",
}

var retailChannels = map[domain.Channel]string{
	domain.ChannelAlfamart:  "ALFAMART",
	domain.ChannelIndomaret: "INDOMARET",
}

func buildPaymentRequest(req gateway.CreatePaymentRequest) (*paymentRequest, error) {
	props := channelProperties{CustomerName: req.Customer.Name}
	if props.CustomerName == "" {
		props.CustomerName = "Customer" // required for VA and retail channels
	}
	if !req.ExpiresAt.IsZero() {
		props.ExpiresAt = req.ExpiresAt.UTC().Format(time.RFC3339)
	}

	pm := paymentMethod{
		Reusability: "ONE_TIME_USE",
		ReferenceID: req.PaymentID, // lets payment_method.* webhooks find the payment
	}
	unsupported := gateway.UnsupportedChannel(domain.ProviderXendit, req.Method, req.Channel)

	switch req.Method {
	case domain.MethodVirtualAccount:
		code, ok := vaChannels[req.Channel]
		if !ok {
			return nil, unsupported
		}
		pm.Type = "VIRTUAL_ACCOUNT"
		pm.VirtualAccount = &channelDetails{ChannelCode: code, ChannelProperties: props}

	case domain.MethodQRIS:
		pm.Type = "QR_CODE"
		pm.QRCode = &channelDetails{ChannelCode: "QRIS", ChannelProperties: channelProperties{ExpiresAt: props.ExpiresAt}}

	case domain.MethodEWallet:
		code, ok := eWalletChannels[req.Channel]
		if !ok {
			return nil, unsupported
		}
		ew := channelProperties{SuccessReturnURL: req.ReturnURL, FailureReturnURL: req.ReturnURL}
		if req.Channel == domain.ChannelOVO {
			if req.Customer.Phone == "" {
				return nil, domain.Invalid("customer.phone", "is required for OVO")
			}
			ew.MobileNumber = req.Customer.Phone
		} else if req.ReturnURL == "" {
			return nil, domain.Invalid("return_url", "is required for "+string(req.Channel))
		}
		pm.Type = "EWALLET"
		pm.EWallet = &channelDetails{ChannelCode: code, ChannelProperties: ew}

	case domain.MethodRetailOutlet:
		code, ok := retailChannels[req.Channel]
		if !ok {
			return nil, unsupported
		}
		pm.Type = "OVER_THE_COUNTER"
		pm.OverTheCounter = &channelDetails{ChannelCode: code, ChannelProperties: props}

	default:
		return nil, unsupported
	}

	return &paymentRequest{
		ReferenceID:   req.PaymentID,
		Amount:        req.Amount,
		Currency:      req.Currency,
		Country:       "ID",
		Description:   req.Description,
		PaymentMethod: pm,
	}, nil
}

func (pm paymentMethod) details() *channelDetails {
	for _, d := range []*channelDetails{pm.VirtualAccount, pm.QRCode, pm.EWallet, pm.OverTheCounter} {
		if d != nil {
			return d
		}
	}
	return nil
}

func (r paymentRequestResponse) action() domain.PaymentAction {
	var a domain.PaymentAction
	if d := r.PaymentMethod.details(); d != nil {
		a.VirtualAccountNumber = d.ChannelProperties.VirtualAccountNumber
		a.QRString = d.ChannelProperties.QRString
		a.PaymentCode = d.ChannelProperties.PaymentCode
	}
	for _, act := range r.Actions {
		switch act.URLType {
		case "DEEPLINK", "MOBILE":
			if a.DeepLinkURL == "" {
				a.DeepLinkURL = act.URL
			}
		case "WEB":
			a.RedirectURL = act.URL
		}
	}
	return a
}

func (r paymentRequestResponse) expiresAt() time.Time {
	d := r.PaymentMethod.details()
	if d == nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, d.ChannelProperties.ExpiresAt)
	return t
}
