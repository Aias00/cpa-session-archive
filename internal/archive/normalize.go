package archive

import "cpa-session-archive/internal/sessionmodel"

// Keep archive query helpers on the shared normalization implementation.
var (
	cleanTurnText                   = sessionmodel.CleanTurnText
	unescapeDisplayText             = sessionmodel.UnescapeDisplayText
	extractResponsePreview          = sessionmodel.ExtractResponsePreview
	addCompletionFacets             = sessionmodel.AddCompletionFacets
	limit                           = sessionmodel.Limit
	sessionID                       = sessionmodel.SessionID
	stableWindowID                  = sessionmodel.StableWindowID
	enrichDesktopMetadata           = sessionmodel.EnrichDesktopMetadata
	extractConversationSummary      = sessionmodel.ExtractConversationSummary
	extractConversationSummaryFirst = sessionmodel.ExtractConversationSummaryFirst
	extractConversationUserText     = sessionmodel.ExtractConversationUserText
	compactSummary                  = sessionmodel.CompactSummary
	enrichGenericFacets             = sessionmodel.EnrichGenericFacets
	extractThreadSource             = sessionmodel.ExtractThreadSource
	firstFacet                      = sessionmodel.FirstFacet
	firstNonEmpty                   = sessionmodel.FirstNonEmpty
	archiveKeyID                    = sessionmodel.ArchiveKeyID
	nativeKeyHash                   = sessionmodel.NativeKeyHash
	sanitizeMeta                    = sessionmodel.SanitizeMeta
	terminalSSEPayload              = sessionmodel.TerminalSSEPayload
)
