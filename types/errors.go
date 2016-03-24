package types

var (
	OK = NewResultOK(nil, "")

	ErrInternalError     = NewError(CodeType_InternalError, "Internal error")
	ErrEncodingError     = NewError(CodeType_EncodingError, "Encoding error")
	ErrBadNonce          = NewError(CodeType_BadNonce, "Error bad nonce")
	ErrUnauthorized      = NewError(CodeType_Unauthorized, "Unauthorized")
	ErrInsufficientFunds = NewError(CodeType_InsufficientFunds, "Insufficient funds")
	ErrUnknownRequest    = NewError(CodeType_UnknownRequest, "Unknown request")

	ErrBaseDuplicateAddress     = NewError(CodeType_BaseDuplicateAddress, "Error (base) duplicate address")
	ErrBaseEncodingError        = NewError(CodeType_BaseEncodingError, "Error (base) encoding error")
	ErrBaseInsufficientFees     = NewError(CodeType_BaseInsufficientFees, "Error (base) insufficient fees")
	ErrBaseInsufficientFunds    = NewError(CodeType_BaseInsufficientFunds, "Error (base) insufficient funds")
	ErrBaseInsufficientGasPrice = NewError(CodeType_BaseInsufficientGasPrice, "Error (base) insufficient gas price")
	ErrBaseInvalidAddress       = NewError(CodeType_BaseInvalidAddress, "Error (base) invalid address")
	ErrBaseInvalidAmount        = NewError(CodeType_BaseInvalidAmount, "Error (base) invalid amount")
	ErrBaseInvalidPubKey        = NewError(CodeType_BaseInvalidPubKey, "Error (base) invalid pubkey")
	ErrBaseInvalidSequence      = NewError(CodeType_BaseInvalidSequence, "Error (base) invalid sequence")
	ErrBaseInvalidSignature     = NewError(CodeType_BaseInvalidSignature, "Error (base) invalid signature")
	ErrBaseUnknownAddress       = NewError(CodeType_BaseUnknownAddress, "Error (base) unknown address")
	ErrBaseUnknownPubKey        = NewError(CodeType_BaseUnknownPubKey, "Error (base) unknown pubkey")
)
