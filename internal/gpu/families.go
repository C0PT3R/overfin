// Package gpu knows which GPUs can decode and encode what, sets Overfin's
// Jellyfin to transcode with the one chosen, and tests that it works.
//
// The families come from the vendors' own tables: NVIDIA's Video Encode
// and Decode GPU Support Matrix, Intel's media-driver feature list, AMD's
// VCN versions. Where a family's boards differ, it says only what all of
// them do: a codec left out is decoded by the processor, slower but never
// broken. The test is what counts.
package gpu

import (
	"slices"

	"github.com/overfin/server/internal/jfsettings"
)

// Codec is a format a GPU decodes or encodes.
type Codec string

const (
	H264   Codec = "H.264"
	HEVC   Codec = "HEVC"
	HEVC10 Codec = "HEVC 10-bit"
	VP9    Codec = "VP9"
	VP910  Codec = "VP9 10-bit"
	AV1    Codec = "AV1"
	MPEG2  Codec = "MPEG-2"
	VC1    Codec = "VC-1"
)

// Family is GPUs that decode and encode the same.
type Family struct {
	ID       string  `json:"id"`
	Vendor   string  `json:"vendor"`
	Name     string  `json:"name"`
	Examples string  `json:"examples"`
	Mode     string  `json:"mode"`   // Jellyfin's hardware acceleration
	Device   bool    `json:"device"` // it needs a device path (/dev/dri/…)
	Decodes  []Codec `json:"decodes"`
	Encodes  []Codec `json:"encodes"`
	Note     string  `json:"note,omitempty"`
}

// Software is no GPU at all: the processor does everything.
const Software = "software"

var (
	nvidiaOld    = []Codec{H264, MPEG2, VC1}
	nvidiaHEVC   = []Codec{H264, HEVC, HEVC10, VP9, MPEG2, VC1}
	nvidiaFull   = []Codec{H264, HEVC, HEVC10, VP9, VP910, MPEG2, VC1}
	nvidiaAV1    = []Codec{H264, HEVC, HEVC10, VP9, VP910, AV1, MPEG2, VC1}
	intelKaby    = []Codec{H264, HEVC, HEVC10, VP9, VP910}
	intelTiger   = []Codec{H264, HEVC, HEVC10, VP9, VP910, AV1}
	amdOld       = []Codec{H264, HEVC, HEVC10}
	amdVCN       = []Codec{H264, HEVC, HEVC10, VP9, VP910}
	amdAV1       = []Codec{H264, HEVC, HEVC10, VP9, VP910, AV1}
	encodeH264   = []Codec{H264}
	encodeHEVC   = []Codec{H264, HEVC}
	encodeHEVC10 = []Codec{H264, HEVC, HEVC10}
	encodeAV1    = []Codec{H264, HEVC, HEVC10, AV1}
)

// Families are the GPUs Overfin knows, software first.
var Families = []Family{
	{ID: Software, Name: "No GPU", Examples: "The processor does all the work", Mode: "none"},

	{ID: "nvidia-maxwell-1", Vendor: "NVIDIA", Name: "Maxwell, first decoder", Examples: "GTX 745, 970, 980, 980 Ti, Titan X",
		Mode: "nvenc", Decodes: nvidiaOld, Encodes: encodeH264},
	{ID: "nvidia-maxwell-2", Vendor: "NVIDIA", Name: "Maxwell, second decoder", Examples: "GTX 750, 750 Ti, 950, 960",
		Mode: "nvenc", Decodes: nvidiaHEVC, Encodes: encodeH264},
	{ID: "nvidia-pascal", Vendor: "NVIDIA", Name: "Pascal", Examples: "GTX 1050–1080 Ti, Quadro P, Tesla P4, P40",
		Mode: "nvenc", Decodes: nvidiaHEVC, Encodes: encodeHEVC10,
		Note: "VP9 10-bit only on some boards, so the processor decodes it. The GT 1030 has no encoder."},
	{ID: "nvidia-turing", Vendor: "NVIDIA", Name: "Volta and Turing", Examples: "GTX 1630, 1650, 1660, RTX 20, Quadro RTX, T400–T1000, Tesla T4",
		Mode: "nvenc", Decodes: nvidiaFull, Encodes: encodeHEVC10},
	{ID: "nvidia-ampere", Vendor: "NVIDIA", Name: "Ampere", Examples: "RTX 30, RTX A2000–A6000, A10, A16, A40",
		Mode: "nvenc", Decodes: nvidiaAV1, Encodes: encodeHEVC10, Note: "The A100 and A30 have no encoder."},
	{ID: "nvidia-ada", Vendor: "NVIDIA", Name: "Ada Lovelace and Blackwell", Examples: "RTX 40, RTX 50, RTX 2000–6000 Ada, L4, L40",
		Mode: "nvenc", Decodes: nvidiaAV1, Encodes: encodeAV1},

	{ID: "intel-skylake", Vendor: "Intel", Name: "Skylake (Gen 9)", Examples: "6th gen Core, HD Graphics 510–580",
		Mode: "qsv", Device: true, Decodes: []Codec{H264, HEVC}, Encodes: encodeHEVC},
	{ID: "intel-apollolake", Vendor: "Intel", Name: "Apollo Lake", Examples: "Celeron J3455, N3450, Pentium J4205",
		Mode: "qsv", Device: true, Decodes: []Codec{H264, HEVC, HEVC10, VP9}, Encodes: encodeHEVC},
	{ID: "intel-kabylake", Vendor: "Intel", Name: "Kaby Lake to Comet Lake (Gen 9.5)", Examples: "7th–10th gen Core, UHD 610–630, Celeron J4105, J5005, N4100",
		Mode: "qsv", Device: true, Decodes: intelKaby, Encodes: encodeHEVC},
	{ID: "intel-icelake", Vendor: "Intel", Name: "Ice Lake and Jasper Lake (Gen 11)", Examples: "10th gen Core i3/i5/i7-10xxG, Celeron N5105, Pentium N6005",
		Mode: "qsv", Device: true, Decodes: intelKaby, Encodes: encodeHEVC10},
	{ID: "intel-tigerlake", Vendor: "Intel", Name: "Tiger Lake to Raptor Lake (Gen 12)", Examples: "11th–14th gen Core, Iris Xe, UHD 730–770, N100, N95, N305",
		Mode: "qsv", Device: true, Decodes: intelTiger, Encodes: encodeHEVC10},
	{ID: "intel-arc", Vendor: "Intel", Name: "Arc and Core Ultra", Examples: "Arc A and B series, Core Ultra (Meteor Lake, Lunar Lake, Arrow Lake)",
		Mode: "qsv", Device: true, Decodes: intelTiger, Encodes: encodeAV1},

	{ID: "amd-polaris", Vendor: "AMD", Name: "Polaris and Vega", Examples: "RX 460–590, RX Vega 56/64, Radeon VII",
		Mode: "vaapi", Device: true, Decodes: amdOld, Encodes: encodeHEVC},
	{ID: "amd-vcn2", Vendor: "AMD", Name: "VCN 1 and 2", Examples: "RX 5000, Ryzen 2000G–5000G and 4000–5000 mobile graphics",
		Mode: "vaapi", Device: true, Decodes: amdVCN, Encodes: encodeHEVC10},
	{ID: "amd-vcn3", Vendor: "AMD", Name: "VCN 3", Examples: "RX 6600–6950, Ryzen 6000 mobile and 7000 desktop graphics",
		Mode: "vaapi", Device: true, Decodes: amdAV1, Encodes: encodeHEVC10},
	{ID: "amd-navi24", Vendor: "AMD", Name: "Navi 24", Examples: "RX 6400, 6500 XT",
		Mode: "vaapi", Device: true, Decodes: amdVCN, Note: "No encoder: the processor encodes."},
	{ID: "amd-vcn4", Vendor: "AMD", Name: "VCN 4 and 5", Examples: "RX 7000, RX 9000, Ryzen 7040, 8000G and AI mobile graphics",
		Mode: "vaapi", Device: true, Decodes: amdAV1, Encodes: encodeAV1},

	{ID: "rockchip-rk3588", Vendor: "Rockchip", Name: "RK3588", Examples: "Orange Pi 5, Rock 5, NanoPC-T6",
		Mode: "rkmpp", Decodes: []Codec{H264, HEVC, HEVC10, VP9, VP910, AV1}, Encodes: encodeHEVC},
}

// Find is the family with id.
func Find(id string) (Family, bool) {
	i := slices.IndexFunc(Families, func(f Family) bool { return f.ID == id })
	if i < 0 {
		return Family{}, false
	}
	return Families[i], true
}

// DefaultDevice is the render device of the first GPU on Linux.
const DefaultDevice = "/dev/dri/renderD128"

// jellyfinCodecs are Jellyfin's names for the codecs it may decode on a GPU.
var jellyfinCodecs = map[Codec]string{H264: "h264", HEVC: "hevc", VP9: "vp9", AV1: "av1", MPEG2: "mpeg2video", VC1: "vc1"}

// Settings are the Jellyfin transcoding settings for family f, with its
// device when it needs one.
func (f Family) Settings(device string) []jfsettings.Setting {
	why := "the GPU chosen in Overfin's panel: " + f.Name
	var decode []string
	for _, c := range f.Decodes {
		if name, ok := jellyfinCodecs[c]; ok {
			decode = append(decode, name)
		}
	}
	if decode == nil {
		decode = []string{}
	}
	s := []jfsettings.Setting{
		{Key: "HardwareAccelerationType", Value: f.Mode, Why: why},
		{Key: "HardwareDecodingCodecs", Value: decode, Why: why},
		{Key: "EnableDecodingColorDepth10Hevc", Value: slices.Contains(f.Decodes, HEVC10), Why: why},
		{Key: "EnableDecodingColorDepth10Vp9", Value: slices.Contains(f.Decodes, VP910), Why: why},
		{Key: "EnableHardwareEncoding", Value: len(f.Encodes) > 0, Why: why},
		{Key: "AllowHevcEncoding", Value: slices.Contains(f.Encodes, HEVC), Why: why},
		{Key: "AllowAv1Encoding", Value: slices.Contains(f.Encodes, AV1), Why: why},
	}
	if f.Device {
		s = append(s, jfsettings.Setting{Key: "VaapiDevice", Value: device, Why: why})
	}
	return s
}
