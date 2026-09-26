// Package nativeidentity is the single read-only native conversation identity
// implementation shared by the API history reader and session recovery.
package nativeidentity

import _ "embed"

//go:embed native_identity.py
var IdentityScript string

//go:embed native_records.py
var RecordsScript string

//go:embed conversations.py
var ConversationsScript string

//go:embed conversation_live.py
var ConversationLiveScript string
