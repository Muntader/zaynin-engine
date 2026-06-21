package types

import vodTypes "github.com/muntader/zaynin-engine/internal/vod/types"

type AnalysisConfig struct {
	Outputs     *vodTypes.Outputs     // pointer so we don't copy the whole thing
	JobSettings *vodTypes.JobSettings
}
