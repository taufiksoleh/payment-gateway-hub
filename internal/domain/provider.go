package domain

// Provider identifies a payment gateway vendor.
type Provider string

const (
	ProviderMidtrans Provider = "midtrans"
	ProviderXendit   Provider = "xendit"
	ProviderDOKU     Provider = "doku"
)

// Environment selects the vendor's sandbox or production endpoints.
type Environment string

const (
	EnvironmentSandbox    Environment = "sandbox"
	EnvironmentProduction Environment = "production"
)

func (e Environment) Valid() bool {
	return e == EnvironmentSandbox || e == EnvironmentProduction
}

// Method is the family of payment instrument the customer uses.
type Method string

const (
	MethodVirtualAccount Method = "virtual_account"
	MethodQRIS           Method = "qris"
	MethodEWallet        Method = "ewallet"
	MethodRetailOutlet   Method = "retail_outlet"
	MethodCard           Method = "card"
)

func (m Method) Valid() bool {
	switch m {
	case MethodVirtualAccount, MethodQRIS, MethodEWallet, MethodRetailOutlet, MethodCard:
		return true
	}
	return false
}

// Channel is the concrete bank, wallet or outlet inside a Method.
// Which channels a provider supports is decided by its adapter.
type Channel string

const (
	// Virtual account banks.
	ChannelBCA     Channel = "bca"
	ChannelBNI     Channel = "bni"
	ChannelBRI     Channel = "bri"
	ChannelMandiri Channel = "mandiri"
	ChannelPermata Channel = "permata"
	ChannelCIMB    Channel = "cimb"
	ChannelBSI     Channel = "bsi"

	// QRIS has a single channel.
	ChannelQRIS Channel = "qris"

	// E-wallets.
	ChannelGoPay     Channel = "gopay"
	ChannelOVO       Channel = "ovo"
	ChannelDANA      Channel = "dana"
	ChannelShopeePay Channel = "shopeepay"
	ChannelLinkAja   Channel = "linkaja"

	// Retail outlets.
	ChannelAlfamart  Channel = "alfamart"
	ChannelIndomaret Channel = "indomaret"

	// Cards (hosted page only).
	ChannelCard Channel = "card"
)
