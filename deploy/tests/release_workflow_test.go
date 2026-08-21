package deploytests

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	On          map[string]any        `yaml:"on"`
	Concurrency map[string]any        `yaml:"concurrency"`
	Permissions map[string]string     `yaml:"permissions"`
	Jobs        map[string]releaseJob `yaml:"jobs"`
}

type releaseJob struct {
	Needs       any               `yaml:"needs"`
	Outputs     map[string]any    `yaml:"outputs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []releaseStep     `yaml:"steps"`
}

type releaseStep struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	Env  map[string]any `yaml:"env"`
	With map[string]any `yaml:"with"`
}

type promotionAsset struct {
	ID     uint64 `json:"id"`
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	Digest string `json:"digest"`
}

type promotionRelease struct {
	ID         uint64           `json:"id"`
	TagName    string           `json:"tag_name"`
	Draft      bool             `json:"draft"`
	Prerelease bool             `json:"prerelease"`
	Assets     []promotionAsset `json:"assets"`
}

type selectedRelease struct {
	ReleaseID uint64                    `json:"release_id"`
	Tag       string                    `json:"tag"`
	Version   string                    `json:"version"`
	Assets    map[string]promotionAsset `json:"assets"`
}

type promotionSelection struct {
	Official selectedRelease `json:"official"`
	Plugin   selectedRelease `json:"plugin"`
	Panel    selectedRelease `json:"panel"`
}

type promotionPaths struct {
	OfficialArchive    string
	OfficialChecksums  string
	OfficialBinary     string
	Plugin             string
	Probe              string
	PluginMetadata     string
	PluginChecksums    string
	PluginSourceCommit string
	PanelAsset         string
	PanelChecksums     string
	PanelManifest      string
	PanelSourceCommit  string
}

type promotionLocked struct {
	Selection               promotionSelection `json:"selection"`
	OfficialArchiveSHA256   string             `json:"official_archive_sha256"`
	OfficialChecksumsSHA256 string             `json:"official_checksums_sha256"`
	OfficialBinarySHA256    string             `json:"official_binary_sha256"`
	PluginSHA256            string             `json:"plugin_sha256"`
	ProbeSHA256             string             `json:"probe_sha256"`
	PluginSourceCommit      string             `json:"plugin_source_commit"`
	PanelAssetSHA256        string             `json:"panel_asset_sha256"`
	PanelManifestSHA256     string             `json:"panel_manifest_sha256"`
	PanelSourceCommit       string             `json:"panel_source_commit"`
	PanelUpstreamTag        string             `json:"panel_upstream_tag"`
	PanelUpstreamCommit     string             `json:"panel_upstream_commit"`
	PanelPatchSHA256        string             `json:"panel_patch_sha256"`
}

type promotionProbeReport struct {
	SchemaVersion    uint32   `json:"schema_version"`
	Compatible       bool     `json:"compatible"`
	Code             string   `json:"code"`
	PluginID         string   `json:"plugin_id,omitempty"`
	PluginVersion    string   `json:"plugin_version,omitempty"`
	MarkerCount      int      `json:"marker_count"`
	ConfigGeneration uint64   `json:"config_generation"`
	ConfigDigest     string   `json:"config_digest"`
	Scenarios        []string `json:"scenarios"`
	FailedScenario   string   `json:"failed_scenario,omitempty"`
}

type promotionResult struct {
	Manifest approvedManifest
	Channel  map[string]any
	Tag      string
	Publish  bool
}

type approvedManifest struct {
	SchemaVersion       uint32                `json:"schema_version"`
	VerifierSchema      uint32                `json:"verifier_schema"`
	Channel             string                `json:"channel"`
	ChannelGeneration   uint64                `json:"channel_generation"`
	PriorFingerprint    *string               `json:"prior_fingerprint"`
	Fingerprint         string                `json:"fingerprint"`
	Official            approvedOfficial      `json:"official"`
	Plugin              approvedPlugin        `json:"plugin"`
	Panel               approvedPanel         `json:"panel"`
	Compatibility       approvedCompatibility `json:"compatibility"`
	ApprovedAttestation approvedAttestation   `json:"approved_attestation"`
}

type approvedManifestV1 struct {
	SchemaVersion       uint32                `json:"schema_version"`
	VerifierSchema      uint32                `json:"verifier_schema"`
	Channel             string                `json:"channel"`
	ChannelGeneration   uint64                `json:"channel_generation"`
	PriorFingerprint    *string               `json:"prior_fingerprint"`
	Fingerprint         string                `json:"fingerprint"`
	Official            approvedOfficial      `json:"official"`
	Plugin              approvedPlugin        `json:"plugin"`
	Compatibility       approvedCompatibility `json:"compatibility"`
	ApprovedAttestation approvedAttestation   `json:"approved_attestation"`
}

type approvedPanel struct {
	Repository     string              `json:"repository"`
	ReleaseID      uint64              `json:"release_id"`
	Tag            string              `json:"tag"`
	UpstreamTag    string              `json:"upstream_tag"`
	UpstreamCommit string              `json:"upstream_commit"`
	PatchSHA256    string              `json:"patch_sha256"`
	Asset          approvedAsset       `json:"asset"`
	Manifest       approvedAsset       `json:"manifest"`
	Attestation    approvedAttestation `json:"attestation"`
}

type approvedAsset struct {
	Name   string `json:"name"`
	ID     uint64 `json:"id"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

type approvedOfficial struct {
	Repository   string        `json:"repository"`
	ReleaseID    uint64        `json:"release_id"`
	Tag          string        `json:"tag"`
	Version      string        `json:"version"`
	Asset        approvedAsset `json:"asset"`
	Checksums    approvedAsset `json:"checksums"`
	BinarySHA256 string        `json:"binary_sha256"`
	Provenance   string        `json:"provenance"`
}

type approvedPlugin struct {
	Repository   string              `json:"repository"`
	ReleaseID    uint64              `json:"release_id"`
	Tag          string              `json:"tag"`
	Version      string              `json:"version"`
	SourceCommit string              `json:"source_commit"`
	Asset        approvedAsset       `json:"asset"`
	ProbeAsset   approvedAsset       `json:"probe_asset"`
	Attestation  approvedAttestation `json:"attestation"`
}

type approvedCompatibility struct {
	SchemaVersion    uint32   `json:"schema_version"`
	Plugin           bool     `json:"plugin"`
	CoreOnly         bool     `json:"core_only"`
	ConfigGeneration uint64   `json:"config_generation"`
	ConfigDigest     string   `json:"config_digest"`
	Scenarios        []string `json:"scenarios"`
}

type approvedAttestation struct {
	Repository   string `json:"repository"`
	Workflow     string `json:"workflow"`
	Ref          string `json:"ref"`
	SourceCommit string `json:"source_commit"`
	Issuer       string `json:"issuer"`
}

type promotionVersion [3]uint64

var promotionSemVerPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var promotionDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var panelTagPattern = regexp.MustCompile(`^panel-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-bridge\.([1-9][0-9]*)$`)

func selectPromotionCandidates(officialRaw, pluginRaw, panelRaw []byte, previous *approvedManifest) (promotionSelection, error) {
	officialReleases, err := decodePromotionReleases(officialRaw)
	if err != nil {
		return promotionSelection{}, fmt.Errorf("official releases: %w", err)
	}
	pluginReleases, err := decodePromotionReleases(pluginRaw)
	if err != nil {
		return promotionSelection{}, fmt.Errorf("plugin releases: %w", err)
	}
	panelReleases, err := decodePromotionReleases(panelRaw)
	if err != nil {
		return promotionSelection{}, fmt.Errorf("panel releases: %w", err)
	}
	official, officialVersion, err := selectPromotionRelease(officialReleases, officialAssetSet)
	if err != nil {
		return promotionSelection{}, fmt.Errorf("official candidate: %w", err)
	}
	plugin, pluginVersion, err := selectPromotionRelease(pluginReleases, pluginAssetSet)
	if err != nil {
		return promotionSelection{}, fmt.Errorf("plugin candidate: %w", err)
	}
	panel, panelVersion, panelRev, err := selectPromotionPanelRelease(panelReleases)
	if err != nil {
		return promotionSelection{}, fmt.Errorf("panel candidate: %w", err)
	}
	if previous != nil {
		previousOfficial, okOfficial := parsePromotionVersion("v" + previous.Official.Version)
		previousPlugin, okPlugin := parsePromotionVersion("v" + previous.Plugin.Version)
		if !okOfficial || !okPlugin {
			return promotionSelection{}, fmt.Errorf("previous approved versions are invalid")
		}
		if comparePromotionVersion(officialVersion, previousOfficial) < 0 ||
			comparePromotionVersion(pluginVersion, previousPlugin) < 0 {
			return promotionSelection{}, fmt.Errorf("candidate downgrade")
		}
		if previous.SchemaVersion >= 2 && previous.Panel.Tag != "" {
			prevPanelVer, prevPanelRev, okPanel := parsePanelVersion(previous.Panel.Tag)
			if !okPanel {
				return promotionSelection{}, fmt.Errorf("previous approved panel version is invalid")
			}
			if comparePanelVersion(panelVersion, panelRev, prevPanelVer, prevPanelRev) < 0 {
				return promotionSelection{}, fmt.Errorf("panel candidate downgrade")
			}
		}
	}
	return promotionSelection{Official: official, Plugin: plugin, Panel: panel}, nil
}

func validatePreviousApprovedManifest(raw []byte) (approvedManifest, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return approvedManifest{}, err
	}
	var header struct {
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return approvedManifest{}, err
	}
	if header.SchemaVersion == 1 {
		var v1 approvedManifestV1
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&v1); err != nil {
			return approvedManifest{}, err
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return approvedManifest{}, err
		}
		if err := validateApprovedManifestV1Fields(v1); err != nil {
			return approvedManifest{}, err
		}
		return approvedManifest{
			SchemaVersion:       v1.SchemaVersion,
			VerifierSchema:      v1.VerifierSchema,
			Channel:             v1.Channel,
			ChannelGeneration:   v1.ChannelGeneration,
			PriorFingerprint:    v1.PriorFingerprint,
			Fingerprint:         v1.Fingerprint,
			Official:            v1.Official,
			Plugin:              v1.Plugin,
			Compatibility:       v1.Compatibility,
			ApprovedAttestation: v1.ApprovedAttestation,
		}, nil
	}
	return validateApprovedManifest(raw)
}

func validateApprovedManifest(raw []byte) (approvedManifest, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return approvedManifest{}, err
	}
	var manifest approvedManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return approvedManifest{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return approvedManifest{}, err
	}
	if err := validateApprovedManifestFields(manifest); err != nil {
		return approvedManifest{}, err
	}
	return manifest, nil
}

func lockPromotionArtifacts(selection promotionSelection, paths promotionPaths) (promotionLocked, error) {
	officialArchive, err := verifyPromotionFile(paths.OfficialArchive, selection.Official.Assets["archive"])
	if err != nil {
		return promotionLocked{}, err
	}
	officialChecksums, err := verifyPromotionFile(paths.OfficialChecksums, selection.Official.Assets["checksums"])
	if err != nil {
		return promotionLocked{}, err
	}
	pluginHash, err := verifyPromotionFile(paths.Plugin, selection.Plugin.Assets["plugin"])
	if err != nil {
		return promotionLocked{}, err
	}
	probeHash, err := verifyPromotionFile(paths.Probe, selection.Plugin.Assets["probe"])
	if err != nil {
		return promotionLocked{}, err
	}
	if _, err := verifyPromotionFile(paths.PluginMetadata, selection.Plugin.Assets["metadata"]); err != nil {
		return promotionLocked{}, err
	}
	if _, err := verifyPromotionFile(paths.PluginChecksums, selection.Plugin.Assets["checksums"]); err != nil {
		return promotionLocked{}, err
	}
	panelAssetHash, err := verifyPromotionFile(paths.PanelAsset, selection.Panel.Assets["asset"])
	if err != nil {
		return promotionLocked{}, err
	}
	panelChecksumsHash, err := verifyPromotionFile(paths.PanelChecksums, selection.Panel.Assets["checksums"])
	if err != nil {
		return promotionLocked{}, err
	}
	panelManifestHash, err := verifyPromotionFile(paths.PanelManifest, selection.Panel.Assets["manifest"])
	if err != nil {
		return promotionLocked{}, err
	}

	officialChecksumRaw, err := os.ReadFile(paths.OfficialChecksums)
	if err != nil || checksumEntry(officialChecksumRaw, selection.Official.Assets["archive"].Name) != officialArchive {
		return promotionLocked{}, fmt.Errorf("official checksum mismatch")
	}
	pluginChecksumRaw, err := os.ReadFile(paths.PluginChecksums)
	if err != nil ||
		checksumEntry(pluginChecksumRaw, selection.Plugin.Assets["plugin"].Name) != pluginHash ||
		checksumEntry(pluginChecksumRaw, selection.Plugin.Assets["probe"].Name) != probeHash {
		return promotionLocked{}, fmt.Errorf("plugin checksum mismatch")
	}
	panelChecksumRaw, err := os.ReadFile(paths.PanelChecksums)
	if err != nil || checksumEntry(panelChecksumRaw, selection.Panel.Assets["asset"].Name) != panelAssetHash {
		return promotionLocked{}, fmt.Errorf("panel checksum mismatch")
	}

	var metadata struct {
		Version      string `json:"version"`
		Tag          string `json:"tag"`
		SourceCommit string `json:"source_commit"`
		Platform     string `json:"platform"`
		ABI          uint32 `json:"abi"`
		RPC          uint32 `json:"rpc"`
		GLIBCMax     string `json:"glibc_max"`
	}
	metadataRaw, err := os.ReadFile(paths.PluginMetadata)
	if err != nil || decodeStrictPromotionJSON(metadataRaw, &metadata) != nil ||
		metadata.Version != selection.Plugin.Version || metadata.Tag != selection.Plugin.Tag ||
		metadata.SourceCommit != paths.PluginSourceCommit || !validLowerCommit(metadata.SourceCommit) ||
		metadata.Platform != "linux-amd64" || metadata.ABI != 1 || metadata.RPC != 3 || (metadata.GLIBCMax != "2.32" && metadata.GLIBCMax != "2.3.2") {
		return promotionLocked{}, fmt.Errorf("plugin metadata mismatch")
	}

	var panelManifest struct {
		SchemaVersion      uint32 `json:"schema_version"`
		SchemaID           string `json:"schema_id"`
		UpstreamRepository string `json:"upstream_repository"`
		UpstreamTag        string `json:"upstream_tag"`
		UpstreamCommit     string `json:"upstream_commit"`
		PatchFile          string `json:"patch_file"`
		PatchSHA256        string `json:"patch_sha256"`
		Asset              struct {
			Name   string `json:"name"`
			Size   uint64 `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"asset"`
	}
	panelManifestRaw, err := os.ReadFile(paths.PanelManifest)
	if err != nil || decodeStrictPromotionJSON(panelManifestRaw, &panelManifest) != nil {
		return promotionLocked{}, fmt.Errorf("panel manifest decode failed")
	}

	panelVer, _, okPanel := parsePanelVersion(selection.Panel.Tag)
	expectedUpstreamTag := fmt.Sprintf("v%d.%d.%d", panelVer[0], panelVer[1], panelVer[2])
	if !okPanel || panelManifest.SchemaVersion != 1 ||
		panelManifest.SchemaID != "cliproxyapi-patched-management-release/v1" ||
		panelManifest.UpstreamRepository != "https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git" ||
		panelManifest.UpstreamTag != expectedUpstreamTag ||
		!validLowerCommit(panelManifest.UpstreamCommit) ||
		!validLowerSHA256(panelManifest.PatchSHA256) ||
		panelManifest.Asset.Name != selection.Panel.Assets["asset"].Name ||
		panelManifest.Asset.Size != selection.Panel.Assets["asset"].Size ||
		panelManifest.Asset.SHA256 != panelAssetHash ||
		!validLowerCommit(paths.PanelSourceCommit) {
		return promotionLocked{}, fmt.Errorf("panel manifest mismatch")
	}

	binaryHash, err := promotionFileHash(paths.OfficialBinary)
	if err != nil {
		return promotionLocked{}, err
	}
	_ = panelChecksumsHash
	return promotionLocked{
		Selection:               selection,
		OfficialArchiveSHA256:   officialArchive,
		OfficialChecksumsSHA256: officialChecksums,
		OfficialBinarySHA256:    binaryHash,
		PluginSHA256:            pluginHash,
		ProbeSHA256:             probeHash,
		PluginSourceCommit:      metadata.SourceCommit,
		PanelAssetSHA256:        panelAssetHash,
		PanelManifestSHA256:     panelManifestHash,
		PanelSourceCommit:       paths.PanelSourceCommit,
		PanelUpstreamTag:        panelManifest.UpstreamTag,
		PanelUpstreamCommit:     panelManifest.UpstreamCommit,
		PanelPatchSHA256:        panelManifest.PatchSHA256,
	}, nil
}

func buildPromotionResult(locked promotionLocked, pluginReport, coreReport promotionProbeReport, previous *approvedManifest, sourceCommit string) (promotionResult, error) {
	if !validLowerCommit(sourceCommit) ||
		pluginReport.SchemaVersion != 1 || !pluginReport.Compatible || pluginReport.Code != "ok" ||
		coreReport.SchemaVersion != 1 || !coreReport.Compatible || coreReport.Code != "ok" ||
		validateApprovedCompatibility(approvedCompatibility{
			SchemaVersion: pluginReport.SchemaVersion,
			Plugin:        true, CoreOnly: true,
			ConfigGeneration: pluginReport.ConfigGeneration,
			ConfigDigest:     pluginReport.ConfigDigest,
			Scenarios:        pluginReport.Scenarios,
		}) != nil {
		return promotionResult{}, fmt.Errorf("compatibility gate failed")
	}
	if previous != nil {
		if _, err := selectPromotionCandidates(
			mustMarshalPromotionReleases([]promotionRelease{selectedToRelease(locked.Selection.Official)}),
			mustMarshalPromotionReleases([]promotionRelease{selectedToRelease(locked.Selection.Plugin)}),
			mustMarshalPromotionReleases([]promotionRelease{selectedToRelease(locked.Selection.Panel)}),
			previous,
		); err != nil {
			return promotionResult{}, err
		}
	}
	generation := uint64(1)
	var prior *string
	if previous != nil {
		generation = previous.ChannelGeneration + 1
		priorValue := previous.Fingerprint
		prior = &priorValue
	}
	officialAsset := locked.Selection.Official.Assets["archive"]
	officialChecksums := locked.Selection.Official.Assets["checksums"]
	pluginAsset := locked.Selection.Plugin.Assets["plugin"]
	probeAsset := locked.Selection.Plugin.Assets["probe"]
	panelAsset := locked.Selection.Panel.Assets["asset"]
	panelManifest := locked.Selection.Panel.Assets["manifest"]

	manifest := approvedManifest{
		SchemaVersion: 2, VerifierSchema: 1, Channel: "stable",
		ChannelGeneration: generation, PriorFingerprint: prior,
		Official: approvedOfficial{
			Repository: "router-for-me/CLIProxyAPI", ReleaseID: locked.Selection.Official.ReleaseID,
			Tag: locked.Selection.Official.Tag, Version: locked.Selection.Official.Version,
			Asset:        approvedAsset{Name: officialAsset.Name, ID: officialAsset.ID, Size: officialAsset.Size, SHA256: locked.OfficialArchiveSHA256},
			Checksums:    approvedAsset{Name: officialChecksums.Name, ID: officialChecksums.ID, Size: officialChecksums.Size, SHA256: locked.OfficialChecksumsSHA256},
			BinarySHA256: locked.OfficialBinarySHA256, Provenance: "official-checksum-only",
		},
		Plugin: approvedPlugin{
			Repository: "jinpeng2700-tech/cpa-plugin-token-saver", ReleaseID: locked.Selection.Plugin.ReleaseID,
			Tag: locked.Selection.Plugin.Tag, Version: locked.Selection.Plugin.Version, SourceCommit: locked.PluginSourceCommit,
			Asset:      approvedAsset{Name: pluginAsset.Name, ID: pluginAsset.ID, Size: pluginAsset.Size, SHA256: locked.PluginSHA256},
			ProbeAsset: approvedAsset{Name: probeAsset.Name, ID: probeAsset.ID, Size: probeAsset.Size, SHA256: locked.ProbeSHA256},
			Attestation: approvedAttestation{
				Repository: "jinpeng2700-tech/cpa-plugin-token-saver", Workflow: ".github/workflows/release.yml",
				Ref: "refs/tags/" + locked.Selection.Plugin.Tag, SourceCommit: locked.PluginSourceCommit,
				Issuer: "https://token.actions.githubusercontent.com",
			},
		},
		Panel: approvedPanel{
			Repository:     "jinpeng2700-tech/cpa-plugin-token-saver",
			ReleaseID:      locked.Selection.Panel.ReleaseID,
			Tag:            locked.Selection.Panel.Tag,
			UpstreamTag:    locked.PanelUpstreamTag,
			UpstreamCommit: locked.PanelUpstreamCommit,
			PatchSHA256:    locked.PanelPatchSHA256,
			Asset:          approvedAsset{Name: panelAsset.Name, ID: panelAsset.ID, Size: panelAsset.Size, SHA256: locked.PanelAssetSHA256},
			Manifest:       approvedAsset{Name: panelManifest.Name, ID: panelManifest.ID, Size: panelManifest.Size, SHA256: locked.PanelManifestSHA256},
			Attestation: approvedAttestation{
				Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
				Workflow:     ".github/workflows/release-panel.yml",
				Ref:          "refs/heads/main",
				SourceCommit: locked.PanelSourceCommit,
				Issuer:       "https://token.actions.githubusercontent.com",
			},
		},
		Compatibility: approvedCompatibility{
			SchemaVersion: 1, Plugin: true, CoreOnly: true,
			ConfigGeneration: pluginReport.ConfigGeneration, ConfigDigest: pluginReport.ConfigDigest,
			Scenarios: pluginReport.Scenarios,
		},
		ApprovedAttestation: approvedAttestation{
			Repository: "jinpeng2700-tech/cpa-plugin-token-saver",
			Workflow:   ".github/workflows/promote-cliproxyapi.yml", Ref: "refs/heads/main",
			SourceCommit: sourceCommit, Issuer: "https://token.actions.githubusercontent.com",
		},
	}
	manifest.Fingerprint = computeApprovedFingerprint(manifest)
	if previous != nil && previous.SchemaVersion == 2 && previous.Fingerprint == manifest.Fingerprint {
		tag := promotionTag(manifest, previous.ChannelGeneration)
		return promotionResult{Manifest: *previous, Channel: promotionChannel(*previous, tag), Tag: tag}, nil
	}
	if _, err := validateApprovedManifest(mustMarshalApprovedManifest(manifest)); err != nil {
		return promotionResult{}, err
	}
	tag := promotionTag(manifest, manifest.ChannelGeneration)
	return promotionResult{Manifest: manifest, Channel: promotionChannel(manifest, tag), Tag: tag, Publish: true}, nil
}

func verifyPromotionFile(name string, asset promotionAsset) (string, error) {
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || uint64(info.Size()) != asset.Size {
		return "", fmt.Errorf("asset size mismatch")
	}
	hash, err := promotionFileHash(name)
	if err != nil || asset.Digest != "sha256:"+hash {
		return "", fmt.Errorf("asset digest mismatch")
	}
	return hash, nil
}

func promotionFileHash(name string) (string, error) {
	raw, err := os.ReadFile(name)
	if err != nil || len(raw) == 0 {
		return "", fmt.Errorf("asset read failed")
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum), nil
}

func checksumEntry(raw []byte, name string) string {
	found := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != name || !validLowerSHA256(strings.ToLower(fields[0])) {
			continue
		}
		if found != "" {
			return ""
		}
		found = strings.ToLower(fields[0])
	}
	return found
}

func decodeStrictPromotionJSON(raw []byte, target any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func selectedToRelease(selected selectedRelease) promotionRelease {
	assets := make([]promotionAsset, 0, len(selected.Assets))
	for _, asset := range selected.Assets {
		assets = append(assets, asset)
	}
	return promotionRelease{ID: selected.ReleaseID, TagName: selected.Tag, Assets: assets}
}

func mustMarshalPromotionReleases(releases []promotionRelease) []byte {
	raw, err := json.Marshal([][]promotionRelease{releases})
	if err != nil {
		panic(err)
	}
	return raw
}

func mustMarshalApprovedManifest(manifest approvedManifest) []byte {
	raw, err := json.Marshal(manifest)
	if err != nil {
		panic(err)
	}
	return raw
}

func promotionChannel(manifest approvedManifest, tag string) map[string]any {
	return map[string]any{
		"schema_version": 1, "channel": "stable",
		"channel_generation": manifest.ChannelGeneration,
		"fingerprint":        manifest.Fingerprint, "approved_tag": tag,
		"manifest_asset": "approved-release.json",
	}
}

func promotionTag(manifest approvedManifest, generation uint64) string {
	panelPart := strings.TrimPrefix(manifest.Panel.Tag, "panel-")
	return "approved-cli-" + manifest.Official.Tag + "-plugin-" + manifest.Plugin.Tag + "-panel-" + panelPart + "-g" + strconv.FormatUint(generation, 10)
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("invalid object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("invalid array")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	return nil
}

func validateApprovedManifestFields(manifest approvedManifest) error {
	if manifest.SchemaVersion != 2 || manifest.VerifierSchema != 1 || manifest.Channel != "stable" ||
		manifest.ChannelGeneration == 0 {
		return fmt.Errorf("invalid manifest header")
	}
	if manifest.ChannelGeneration == 1 {
		if manifest.PriorFingerprint != nil {
			return fmt.Errorf("genesis manifest has a predecessor")
		}
	} else if manifest.PriorFingerprint == nil || !validPromotionFingerprint(*manifest.PriorFingerprint) {
		return fmt.Errorf("manifest predecessor is missing")
	}
	if err := validateApprovedOfficial(manifest.Official); err != nil {
		return err
	}
	if err := validateApprovedPlugin(manifest.Plugin); err != nil {
		return err
	}
	if err := validateApprovedPanel(manifest.Panel); err != nil {
		return err
	}
	if err := validateApprovedCompatibility(manifest.Compatibility); err != nil {
		return err
	}
	if !validAttestation(
		manifest.ApprovedAttestation,
		"jinpeng2700-tech/cpa-plugin-token-saver",
		".github/workflows/promote-cliproxyapi.yml",
		"refs/heads/main",
		manifest.ApprovedAttestation.SourceCommit,
	) {
		return fmt.Errorf("invalid approved attestation identity")
	}
	if manifest.Fingerprint != computeApprovedFingerprint(manifest) {
		return fmt.Errorf("approved fingerprint mismatch")
	}
	return nil
}

func validateApprovedManifestV1Fields(manifest approvedManifestV1) error {
	if manifest.SchemaVersion != 1 || manifest.VerifierSchema != 1 || manifest.Channel != "stable" ||
		manifest.ChannelGeneration == 0 {
		return fmt.Errorf("invalid manifest header")
	}
	if manifest.ChannelGeneration == 1 {
		if manifest.PriorFingerprint != nil {
			return fmt.Errorf("genesis manifest has a predecessor")
		}
	} else if manifest.PriorFingerprint == nil || !validPromotionFingerprint(*manifest.PriorFingerprint) {
		return fmt.Errorf("manifest predecessor is missing")
	}
	if err := validateApprovedOfficial(manifest.Official); err != nil {
		return err
	}
	if err := validateApprovedPlugin(manifest.Plugin); err != nil {
		return err
	}
	if err := validateApprovedCompatibility(manifest.Compatibility); err != nil {
		return err
	}
	if !validAttestation(
		manifest.ApprovedAttestation,
		"jinpeng2700-tech/cpa-plugin-token-saver",
		".github/workflows/promote-cliproxyapi.yml",
		"refs/heads/main",
		manifest.ApprovedAttestation.SourceCommit,
	) {
		return fmt.Errorf("invalid approved attestation identity")
	}
	return nil
}

func validateApprovedPanel(panel approvedPanel) error {
	ver, rev, ok := parsePanelVersion(panel.Tag)
	if !ok || rev == 0 {
		return fmt.Errorf("invalid panel tag")
	}
	expectedUpstream := fmt.Sprintf("v%d.%d.%d", ver[0], ver[1], ver[2])
	if panel.UpstreamTag != expectedUpstream {
		return fmt.Errorf("invalid panel upstream tag")
	}
	if panel.Repository != "jinpeng2700-tech/cpa-plugin-token-saver" || panel.ReleaseID == 0 ||
		!validLowerCommit(panel.UpstreamCommit) || !validLowerSHA256(panel.PatchSHA256) ||
		panel.Asset.Name != "management.html" || panel.Manifest.Name != "panel-manifest.json" ||
		!validApprovedAsset(panel.Asset) || !validApprovedAsset(panel.Manifest) ||
		!validAttestation(
			panel.Attestation,
			panel.Repository,
			".github/workflows/release-panel.yml",
			"refs/heads/main",
			panel.Attestation.SourceCommit,
		) {
		return fmt.Errorf("invalid panel identity")
	}
	return nil
}

func validateApprovedOfficial(official approvedOfficial) error {
	version, ok := parsePromotionVersion(official.Tag)
	if !ok || official.Version != strings.TrimPrefix(official.Tag, "v") ||
		comparePromotionVersion(version, promotionVersion{}) <= 0 {
		return fmt.Errorf("invalid official version")
	}
	if official.Repository != "router-for-me/CLIProxyAPI" || official.ReleaseID == 0 ||
		official.Provenance != "official-checksum-only" ||
		official.Asset.Name != "CLIProxyAPI_"+official.Version+"_linux_amd64.tar.gz" ||
		official.Checksums.Name != "checksums.txt" ||
		!validApprovedAsset(official.Asset) || !validApprovedAsset(official.Checksums) ||
		!validLowerSHA256(official.BinarySHA256) {
		return fmt.Errorf("invalid official identity")
	}
	return nil
}

func validateApprovedPlugin(plugin approvedPlugin) error {
	version, ok := parsePromotionVersion(plugin.Tag)
	if !ok || plugin.Version != strings.TrimPrefix(plugin.Tag, "v") ||
		comparePromotionVersion(version, promotionVersion{}) <= 0 {
		return fmt.Errorf("invalid plugin version")
	}
	if plugin.Repository != "jinpeng2700-tech/cpa-plugin-token-saver" || plugin.ReleaseID == 0 ||
		!validLowerCommit(plugin.SourceCommit) ||
		plugin.Asset.Name != "token-saver-v"+plugin.Version+"-linux-amd64.so" ||
		plugin.ProbeAsset.Name != "compat-probe-v"+plugin.Version+"-linux-amd64" ||
		!validApprovedAsset(plugin.Asset) || !validApprovedAsset(plugin.ProbeAsset) ||
		!validAttestation(
			plugin.Attestation,
			plugin.Repository,
			".github/workflows/release.yml",
			"refs/tags/"+plugin.Tag,
			plugin.SourceCommit,
		) {
		return fmt.Errorf("invalid plugin identity")
	}
	return nil
}

func validateApprovedCompatibility(compatibility approvedCompatibility) error {
	wantScenarios := "all-off,rtk,headroom-rewrite,headroom-timeout,caveman,ponytail,fixed-order"
	if compatibility.SchemaVersion != 1 || !compatibility.Plugin || !compatibility.CoreOnly ||
		compatibility.ConfigGeneration == 0 || !validLowerSHA256(compatibility.ConfigDigest) ||
		strings.Join(compatibility.Scenarios, ",") != wantScenarios {
		return fmt.Errorf("invalid compatibility evidence")
	}
	return nil
}

func validApprovedAsset(asset approvedAsset) bool {
	return asset.ID > 0 && asset.Size > 0 && validArtifactName(asset.Name) && validLowerSHA256(asset.SHA256)
}

func validArtifactName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.ContainsAny(name, `/\`) &&
		name != "." && name != ".." && strings.IndexFunc(name, unicode.IsControl) < 0
}

func validAttestation(attestation approvedAttestation, repository, workflow, ref, sourceCommit string) bool {
	return attestation.Repository == repository &&
		attestation.Workflow == workflow &&
		attestation.Ref == ref &&
		attestation.SourceCommit == sourceCommit &&
		validLowerCommit(attestation.SourceCommit) &&
		attestation.Issuer == "https://token.actions.githubusercontent.com"
}

func validLowerSHA256(value string) bool {
	return regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(value)
}

func validLowerCommit(value string) bool {
	return regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(value)
}

func validPromotionFingerprint(value string) bool {
	return promotionDigestPattern.MatchString(value)
}

func computeApprovedFingerprint(manifest approvedManifest) string {
	material := struct {
		SchemaVersion       uint32                `json:"schema_version"`
		VerifierSchema      uint32                `json:"verifier_schema"`
		Channel             string                `json:"channel"`
		Official            approvedOfficial      `json:"official"`
		Plugin              approvedPlugin        `json:"plugin"`
		Panel               approvedPanel         `json:"panel"`
		Compatibility       approvedCompatibility `json:"compatibility"`
		ApprovedAttestation approvedAttestation   `json:"approved_attestation"`
	}{
		SchemaVersion:       manifest.SchemaVersion,
		VerifierSchema:      manifest.VerifierSchema,
		Channel:             manifest.Channel,
		Official:            manifest.Official,
		Plugin:              manifest.Plugin,
		Panel:               manifest.Panel,
		Compatibility:       manifest.Compatibility,
		ApprovedAttestation: manifest.ApprovedAttestation,
	}
	raw, err := json.Marshal(material)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum)
}

func decodePromotionReleases(raw []byte) ([]promotionRelease, error) {
	var pages [][]promotionRelease
	if err := json.Unmarshal(raw, &pages); err == nil {
		var releases []promotionRelease
		for _, page := range pages {
			releases = append(releases, page...)
		}
		return releases, nil
	}
	var releases []promotionRelease
	if err := json.Unmarshal(raw, &releases); err != nil {
		return nil, err
	}
	return releases, nil
}

func officialAssetSet(version string) map[string]string {
	return map[string]string{
		"archive":   "CLIProxyAPI_" + version + "_linux_amd64.tar.gz",
		"checksums": "checksums.txt",
	}
}

func pluginAssetSet(version string) map[string]string {
	return map[string]string{
		"plugin":    "token-saver-v" + version + "-linux-amd64.so",
		"probe":     "compat-probe-v" + version + "-linux-amd64",
		"metadata":  "release-metadata.json",
		"checksums": "SHA256SUMS",
	}
}

func panelAssetSet() map[string]string {
	return map[string]string{
		"asset":     "management.html",
		"checksums": "management.html.sha256",
		"manifest":  "panel-manifest.json",
	}
}

func selectPromotionPanelRelease(releases []promotionRelease) (selectedRelease, promotionVersion, uint64, error) {
	var selected selectedRelease
	var selectedVer promotionVersion
	var selectedRev uint64
	found := false
	for _, release := range releases {
		ver, rev, ok := parsePanelVersion(release.TagName)
		if !ok || release.Draft || release.Prerelease || release.ID == 0 {
			continue
		}
		assets, ok := selectPromotionAssets(release.Assets, panelAssetSet())
		if !ok {
			continue
		}
		if found && comparePanelVersion(ver, rev, selectedVer, selectedRev) <= 0 {
			continue
		}
		selected = selectedRelease{
			ReleaseID: release.ID,
			Tag:       release.TagName,
			Version:   fmt.Sprintf("v%d.%d.%d-bridge.%d", ver[0], ver[1], ver[2], rev),
			Assets:    assets,
		}
		selectedVer = ver
		selectedRev = rev
		found = true
	}
	if !found {
		return selectedRelease{}, promotionVersion{}, 0, fmt.Errorf("no valid stable panel release")
	}
	return selected, selectedVer, selectedRev, nil
}

func selectPromotionRelease(releases []promotionRelease, required func(string) map[string]string) (selectedRelease, promotionVersion, error) {
	var selected selectedRelease
	var selectedVersion promotionVersion
	found := false
	for _, release := range releases {
		version, ok := parsePromotionVersion(release.TagName)
		if !ok || release.Draft || release.Prerelease || release.ID == 0 {
			continue
		}
		versionText := strings.TrimPrefix(release.TagName, "v")
		assets, ok := selectPromotionAssets(release.Assets, required(versionText))
		if !ok {
			continue
		}
		if found && comparePromotionVersion(version, selectedVersion) <= 0 {
			continue
		}
		selected = selectedRelease{
			ReleaseID: release.ID,
			Tag:       release.TagName,
			Version:   versionText,
			Assets:    assets,
		}
		selectedVersion = version
		found = true
	}
	if !found {
		return selectedRelease{}, promotionVersion{}, fmt.Errorf("no valid stable release")
	}
	return selected, selectedVersion, nil
}

func selectPromotionAssets(assets []promotionAsset, required map[string]string) (map[string]promotionAsset, bool) {
	selected := make(map[string]promotionAsset, len(required))
	for key, name := range required {
		for _, asset := range assets {
			if asset.Name != name {
				continue
			}
			if _, duplicate := selected[key]; duplicate ||
				asset.ID == 0 || asset.Size == 0 || !promotionDigestPattern.MatchString(asset.Digest) {
				return nil, false
			}
			selected[key] = asset
		}
		if _, ok := selected[key]; !ok {
			return nil, false
		}
	}
	return selected, true
}

func parsePanelVersion(tag string) (promotionVersion, uint64, bool) {
	match := panelTagPattern.FindStringSubmatch(tag)
	if match == nil {
		return promotionVersion{}, 0, false
	}
	v1, err1 := strconv.ParseUint(match[1], 10, 64)
	v2, err2 := strconv.ParseUint(match[2], 10, 64)
	v3, err3 := strconv.ParseUint(match[3], 10, 64)
	rev, err4 := strconv.ParseUint(match[4], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || rev == 0 {
		return promotionVersion{}, 0, false
	}
	return promotionVersion{v1, v2, v3}, rev, true
}

func comparePanelVersion(leftVer promotionVersion, leftRev uint64, rightVer promotionVersion, rightRev uint64) int {
	c := comparePromotionVersion(leftVer, rightVer)
	if c != 0 {
		return c
	}
	if leftRev < rightRev {
		return -1
	}
	if leftRev > rightRev {
		return 1
	}
	return 0
}

func parsePromotionVersion(tag string) (promotionVersion, bool) {
	match := promotionSemVerPattern.FindStringSubmatch(tag)
	if match == nil {
		return promotionVersion{}, false
	}
	var version promotionVersion
	for index := range version {
		value, err := strconv.ParseUint(match[index+1], 10, 64)
		if err != nil {
			return promotionVersion{}, false
		}
		version[index] = value
	}
	return version, true
}

func comparePromotionVersion(left, right promotionVersion) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github/workflows/release.yml")), &workflow); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	return workflow
}

func readPromotionWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github/workflows/promote-cliproxyapi.yml")), &workflow); err != nil {
		t.Fatalf("parse promotion workflow: %v", err)
	}
	return workflow
}

func readPanelWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github/workflows/release-panel.yml")), &workflow); err != nil {
		t.Fatalf("parse release-panel workflow: %v", err)
	}
	return workflow
}

func requireReleaseJob(t *testing.T, workflow releaseWorkflow, name string) releaseJob {
	t.Helper()
	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("release workflow is missing %q job", name)
	}
	return job
}

func requireActionStep(t *testing.T, job releaseJob, action string) releaseStep {
	t.Helper()
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			return step
		}
	}
	t.Fatalf("job is missing %s action", action)
	return releaseStep{}
}

func requireNamedStep(t *testing.T, job releaseJob, name string) releaseStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("job is missing %q step", name)
	return releaseStep{}
}

func joinedRun(job releaseJob) string {
	var commands []string
	for _, step := range job.Steps {
		commands = append(commands, step.Run)
	}
	return strings.Join(commands, "\n")
}

func workflowValue(values map[string]any, key string) string {
	return fmt.Sprint(values[key])
}

func jobNeeds(job releaseJob, dependency string) bool {
	switch needs := job.Needs.(type) {
	case string:
		return needs == dependency
	case []any:
		for _, need := range needs {
			if fmt.Sprint(need) == dependency {
				return true
			}
		}
	}
	return false
}

func TestReleaseWorkflowPublishesOnlyAfterReadOnlyCompatibility(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")
	publish := requireReleaseJob(t, workflow, "publish")

	if workflow.Permissions["contents"] != "read" {
		t.Fatal("workflow must grant read-only contents permission")
	}
	for name, job := range workflow.Jobs {
		for permission, access := range job.Permissions {
			if access == "write" && name != "publish" {
				t.Fatalf("%s job has write permission %s", name, permission)
			}
		}
	}
	if len(workflow.Jobs) != 3 {
		t.Fatalf("release workflow jobs = %d, want build, compatibility, and publish", len(workflow.Jobs))
	}
	if compatibility.Permissions["contents"] != "read" || compatibility.Permissions["actions"] != "read" {
		t.Fatal("compatibility job must have explicit read-only contents and actions permissions")
	}
	if !jobNeeds(compatibility, "build") {
		t.Fatal("compatibility job must wait for build")
	}
	for permission, want := range map[string]string{
		"actions":      "read",
		"attestations": "write",
		"contents":     "write",
		"id-token":     "write",
	} {
		if publish.Permissions[permission] != want {
			t.Fatalf("publish permission %s = %q, want %q", permission, publish.Permissions[permission], want)
		}
	}
	if !jobNeeds(publish, "compatibility") {
		t.Fatal("publish job must wait for compatibility")
	}

	compatibilityRun := joinedRun(compatibility)
	if !strings.Contains(compatibilityRun, "compat-probe") {
		t.Fatal("read-only compatibility job must execute the downloaded candidate")
	}
	dispatch := requireNamedStep(t, compatibility, "Prove real host dispatch on baseline and fixed v7.2.137")
	if !strings.Contains(dispatch.Run, `"$compat_probe" -candidate`) || !strings.Contains(dispatch.Run, "TestRealCandidate") {
		t.Fatal("compatibility job must execute both compatibility probe and real host-dispatch tests")
	}
	publishRun := joinedRun(publish)
	for _, want := range []string{"sha256sum -c SHA256SUMS", "release-metadata.json", "gh release create", "--verify-tag"} {
		if !strings.Contains(publishRun, want) {
			t.Errorf("publish job missing %q", want)
		}
	}
	if strings.Contains(publishRun, "gh release upload") || strings.Contains(publishRun, "--clobber") {
		t.Fatal("publish job must not mutate an existing release")
	}
	for _, step := range publish.Steps {
		if strings.Contains(step.Run, `"$compat_probe"`) || strings.Contains(step.Run, "TestRealCandidate") {
			t.Fatalf("publish step %q runs compatibility after release creation", step.Name)
		}
	}
	checkout := requireActionStep(t, compatibility, "actions/checkout")
	if workflowValue(checkout.With, "persist-credentials") != "false" {
		t.Fatal("compatibility checkout must not persist even its read-only token")
	}
	for _, step := range compatibility.Steps {
		if !strings.Contains(step.Run, "compat-probe") && !strings.Contains(step.Run, "TestRealCandidate") {
			continue
		}
		if _, ok := step.Env["GH_TOKEN"]; ok || strings.Contains(step.Run, "GH_TOKEN") || strings.Contains(step.Run, "github.token") || strings.Contains(step.Run, "secrets.") {
			t.Fatalf("candidate execution step %q exposes a repository token or secret", step.Name)
		}
	}
	ci := readRepositoryFile(t, ".github/workflows/ci.yml")
	if !strings.Contains(ci, "tags-ignore:") || !strings.Contains(ci, `- "v*"`) {
		t.Fatal("ordinary CI must ignore release tags to avoid duplicate builds")
	}
	_ = build
}

func TestCIWorkflowUsesReadOnlyPermissions(t *testing.T) {
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github/workflows/ci.yml")), &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	if workflow.Permissions["contents"] != "read" {
		t.Fatal("CI workflow must grant read-only contents permission")
	}
	for name, job := range workflow.Jobs {
		for permission, access := range job.Permissions {
			if access == "write" {
				t.Fatalf("CI job %s has write permission %s", name, permission)
			}
		}
	}
}

func TestAllWorkflowCheckoutsDisableCredentialPersistence(t *testing.T) {
	for _, workflowPath := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml", ".github/workflows/release-panel.yml"} {
		var workflow releaseWorkflow
		if err := yaml.Unmarshal([]byte(readRepositoryFile(t, workflowPath)), &workflow); err != nil {
			t.Fatalf("parse %s: %v", workflowPath, err)
		}
		for name, job := range workflow.Jobs {
			for _, step := range job.Steps {
				if !strings.HasPrefix(step.Uses, "actions/checkout@") {
					continue
				}
				if workflowValue(step.With, "persist-credentials") != "false" {
					t.Errorf("%s job %s checkout persists credentials", workflowPath, name)
				}
			}
		}
	}
}

func TestReleaseWorkflowAttestsExactlyPublishedArtifacts(t *testing.T) {
	publish := requireReleaseJob(t, readReleaseWorkflow(t), "publish")
	attest := requireActionStep(t, publish, "actions/attest-build-provenance")
	if attest.Uses != "actions/attest-build-provenance@e8998f949152b193b063cb0ec769d69d929409be" {
		t.Fatalf("attestation action = %q", attest.Uses)
	}
	if workflowValue(attest.With, "subject-path") != "dist/*" {
		t.Fatalf("attestation subject = %q, want dist/*", workflowValue(attest.With, "subject-path"))
	}

	attestIndex, publishIndex := -1, -1
	for index, step := range publish.Steps {
		if step.Uses == attest.Uses {
			attestIndex = index
		}
		if strings.Contains(step.Run, "gh release create") {
			publishIndex = index
			if !strings.Contains(step.Run, `gh release create "$GITHUB_REF_NAME" dist/*`) {
				t.Fatal("published files must use the same validated dist/* set as attestation")
			}
		}
	}
	if attestIndex < 0 || publishIndex < 0 || attestIndex >= publishIndex {
		t.Fatal("attestation must complete before release publication")
	}
}

func TestReleaseWorkflowBindsEventBuildMetadataAndRemoteTagCommits(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	publish := requireReleaseJob(t, workflow, "publish")

	for _, output := range []string{"event_commit", "source_commit"} {
		if !strings.Contains(workflowValue(build.Outputs, output), "steps.release_identity.outputs."+output) {
			t.Errorf("build output %s is not bound to release_identity", output)
		}
	}
	identity := requireNamedStep(t, build, "Bind event and build commits")
	for _, want := range []string{`"${GITHUB_SHA}^{commit}"`, "HEAD^{commit}", `"event_commit=$event_commit"`, `"source_commit=$source_commit"`} {
		if !strings.Contains(identity.Run, want) {
			t.Errorf("build identity step missing %q", want)
		}
	}
	publishIdentity := requireNamedStep(t, publish, "Bind event, build, metadata, and remote tag commits")
	for _, want := range []string{
		"scripts/verify-release-identity.sh",
		"needs.build.outputs.event_commit",
		"needs.build.outputs.source_commit",
		"release-metadata.json",
		"GITHUB_REPOSITORY",
		"GITHUB_REF_NAME",
	} {
		if !strings.Contains(publishIdentity.Run+fmt.Sprint(publishIdentity.Env), want) {
			t.Errorf("publish identity step missing %q", want)
		}
	}
}

func TestReleaseWorkflowVerifiesOfficialHostIdentityBeforeExecution(t *testing.T) {
	compatibility := requireReleaseJob(t, readReleaseWorkflow(t), "compatibility")
	download := requireNamedStep(t, compatibility, "Download and verify official candidates without executing them")
	for _, want := range []string{
		`select(.name == $asset)`,
		`select(.name == "checksums.txt")`,
		".id",
		".size",
		"/releases/assets/${asset_id}",
		"/releases/assets/${checksums_id}",
		`stat -c '%s'`,
		"official_sha256",
		"sha256sum",
		"candidate.tar.gz",
		"checksums.txt",
	} {
		if !strings.Contains(download.Run, want) {
			t.Errorf("official candidate verification missing %q", want)
		}
	}
	for _, ordered := range [][2]string{
		{"asset_id=", "candidate.tar.gz"},
		{"asset_size=", "stat -c '%s'"},
		{"official_sha256=", "tar -xzf"},
	} {
		if strings.Index(download.Run, ordered[0]) >= strings.Index(download.Run, ordered[1]) {
			t.Errorf("%q must occur before %q", ordered[0], ordered[1])
		}
	}
}

func TestReleaseWorkflowUsesFreshImmutableBuildArtifact(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	build := requireReleaseJob(t, workflow, "build")
	compatibility := requireReleaseJob(t, workflow, "compatibility")
	publish := requireReleaseJob(t, workflow, "publish")

	upload := requireActionStep(t, build, "actions/upload-artifact")
	compatibilityDownload := requireActionStep(t, compatibility, "actions/download-artifact")
	publishDownload := requireActionStep(t, publish, "actions/download-artifact")
	artifactName := workflowValue(upload.With, "name")
	if artifactName == "" || !strings.Contains(artifactName, "github.run_id") || !strings.Contains(artifactName, "github.run_attempt") {
		t.Fatalf("build artifact name %q is not unique to this run attempt", artifactName)
	}
	if workflowValue(upload.With, "overwrite") == "true" {
		t.Fatal("release build artifact must be immutable")
	}
	uploadPath := workflowValue(upload.With, "path")
	for _, want := range []string{
		"token-saver-v1.1.0-linux-amd64.so",
		"compat-probe-v1.1.0-linux-amd64",
		"update-verifier-v1.1.0-linux-amd64",
		"GLIBC_REQUIREMENTS.txt",
		"release-metadata.json",
		"SHA256SUMS",
	} {
		if !strings.Contains(uploadPath, want) {
			t.Errorf("release upload path missing %q", want)
		}
	}
	for _, forbidden := range []string{"source.tar.gz", "token-saver.h", "\ndist/\n"} {
		if strings.Contains("\n"+uploadPath+"\n", forbidden) {
			t.Fatalf("release upload path includes dirty or broad path %q", forbidden)
		}
	}
	if !strings.Contains(workflowValue(build.Outputs, "manifest_sha256"), "steps.release_manifest.outputs.sha256") {
		t.Fatal("build must bind downstream jobs to the original checksum manifest digest")
	}
	if workflowValue(compatibilityDownload.With, "name") != artifactName {
		t.Fatal("compatibility must download the exact build artifact")
	}
	if workflowValue(publishDownload.With, "name") != artifactName {
		t.Fatal("publish must download the exact compatibility-tested build artifact")
	}

	compatibilityRun := joinedRun(compatibility)
	if !strings.Contains(compatibilityRun, `candidate_root="$RUNNER_TEMP/candidate-artifact"`) ||
		!strings.Contains(compatibilityRun, `cp -a "$source_root/." "$candidate_root/"`) ||
		!strings.Contains(compatibilityRun, `rm -rf "$source_root"`) {
		t.Fatal("compatibility must execute only a disposable copy of its downloaded artifact")
	}
	if !strings.Contains(compatibilityRun, `token-saver.so`) {
		t.Fatal("real compatibility probe must exercise the production stable plugin filename")
	}
	if !strings.Contains(compatibilityRun, "sha256sum -c SHA256SUMS") {
		t.Fatal("compatibility must verify the freshly downloaded build artifact")
	}
	if !strings.Contains(compatibilityRun, "EXPECTED_MANIFEST_SHA256") {
		t.Fatal("compatibility must compare the downloaded manifest with the build job output")
	}
}

func TestPanelReleaseWorkflowIsScheduledManualReadOnlyAndAttested(t *testing.T) {
	workflow := readPanelWorkflow(t)

	schedule, ok := workflow.On["schedule"].([]any)
	if !ok || len(schedule) != 1 {
		t.Fatalf("panel release schedule = %#v, want one cron entry", workflow.On["schedule"])
	}
	entry, ok := schedule[0].(map[string]any)
	if !ok || workflowValue(entry, "cron") != "43 */6 * * *" {
		t.Fatalf("panel release cron = %q, want 43 */6 * * *", workflowValue(entry, "cron"))
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("panel release workflow missing workflow_dispatch trigger")
	}

	if workflow.Permissions["contents"] != "read" {
		t.Fatalf("panel release top-level contents permission = %q, want read", workflow.Permissions["contents"])
	}

	build := requireReleaseJob(t, workflow, "build")
	publish := requireReleaseJob(t, workflow, "publish")

	if !jobNeeds(publish, "build") {
		t.Fatal("panel release publish job must depend on build job")
	}

	if build.Permissions != nil && len(build.Permissions) > 0 {
		for perm, val := range build.Permissions {
			if val == "write" {
				t.Fatalf("panel release build job has write permission: %s=%s", perm, val)
			}
		}
	}

	wantPublishPermissions := map[string]string{
		"actions":      "read",
		"attestations": "write",
		"contents":     "write",
		"id-token":     "write",
	}
	for k, want := range wantPublishPermissions {
		if publish.Permissions[k] != want {
			t.Fatalf("panel publish permission %s = %q, want %q", k, publish.Permissions[k], want)
		}
	}
	for k := range publish.Permissions {
		if _, ok := wantPublishPermissions[k]; !ok {
			t.Fatalf("panel publish has unexpected permission: %s", k)
		}
	}

	buildRun := joinedRun(build)
	for _, want := range []string{
		"gh api --paginate --slurp repos/router-for-me/Cli-Proxy-API-Management-Center/releases",
		"panel/build-panel.py",
		"management.html",
		"management.html.sha256",
		"panel-manifest.json",
	} {
		if !strings.Contains(buildRun, want) {
			t.Errorf("panel release build job missing %q", want)
		}
	}
	if strings.Contains(buildRun, "/releases/latest") {
		t.Fatal("panel release build must not use /releases/latest")
	}

	publishRun := joinedRun(publish)
	for _, want := range []string{
		"gh release create",
		"gh release view",
	} {
		if !strings.Contains(publishRun, want) {
			t.Errorf("panel release publish job missing %q", want)
		}
	}
	if strings.Contains(publishRun, "--clobber") || strings.Contains(publishRun, "gh release upload") {
		t.Fatal("panel release publish contains forbidden mutable flag/command")
	}

	attestIndex, publishIndex := -1, -1
	for index, step := range publish.Steps {
		if strings.HasPrefix(step.Uses, "actions/attest-build-provenance@") {
			attestIndex = index
		}
		if strings.Contains(step.Run, "gh release create") {
			publishIndex = index
		}
	}
	if attestIndex == -1 || publishIndex == -1 || attestIndex >= publishIndex {
		t.Fatalf("panel release attestation must precede release creation (attest=%d, publish=%d)", attestIndex, publishIndex)
	}
}

func TestPanelReleaseWorkflowActionPinning(t *testing.T) {
	fullSHA := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	workflow := readPanelWorkflow(t)
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			if !fullSHA.MatchString(step.Uses) {
				t.Errorf("panel workflow action is not pinned to full commit SHA: %s", step.Uses)
			}
		}
	}
}

func TestPromotionControlPlaneFilesExist(t *testing.T) {
	for _, name := range []string{
		".github/workflows/promote-cliproxyapi.yml",
		"deploy/channel.json",
		"deploy/approved-release.schema.json",
		"deploy/trust-policy.json",
	} {
		t.Run(name, func(t *testing.T) {
			_ = readRepositoryFile(t, name)
		})
	}
}

func TestPromotionWorkflowIsScheduledManualSerializedAndReadOnlyBeforePublish(t *testing.T) {
	workflow := readPromotionWorkflow(t)
	raw := readRepositoryFile(t, ".github/workflows/promote-cliproxyapi.yml")
	if _, ok := workflow.On["schedule"]; !ok {
		t.Fatal("promotion workflow must have a schedule trigger")
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("promotion workflow must support workflow_dispatch")
	}
	if workflowValue(workflow.Concurrency, "group") == "" || workflowValue(workflow.Concurrency, "cancel-in-progress") != "false" {
		t.Fatal("promotion workflow must serialize without canceling an in-progress promotion")
	}
	if workflow.Permissions["contents"] != "read" {
		t.Fatal("promotion workflow must default to read-only contents")
	}
	if !strings.Contains(raw, `cron: "17 */6 * * *"`) {
		t.Fatal("promotion schedule must use a non-hour cron")
	}
	publish := requireReleaseJob(t, workflow, "publish")
	if !jobNeeds(publish, "compatibility") {
		t.Fatal("publish must wait for compatibility")
	}
	for name, job := range workflow.Jobs {
		for permission, access := range job.Permissions {
			if access == "write" && name != "publish" {
				t.Fatalf("%s job has write permission %s", name, permission)
			}
		}
	}
	for permission, want := range map[string]string{
		"actions":      "read",
		"attestations": "write",
		"contents":     "write",
		"id-token":     "write",
	} {
		if publish.Permissions[permission] != want {
			t.Fatalf("publish permission %s = %q, want %q", permission, publish.Permissions[permission], want)
		}
	}
}

func TestPromotionSelectionChoosesHighestStableSemVerAndLocksAssetIdentity(t *testing.T) {
	official := []promotionRelease{
		officialReleaseFixture(100, "v7.2.9"),
		officialReleaseFixture(101, "v7.2.10"),
		{ID: 102, TagName: "v9.0.0", Draft: true},
		{ID: 103, TagName: "v8.0.0", Prerelease: true},
		officialReleaseFixture(104, "v7.02.11"),
	}
	plugin := []promotionRelease{
		pluginReleaseFixture(200, "v1.0.0"),
		pluginReleaseFixture(201, "v1.0.2"),
		{ID: 202, TagName: "v1.1.0", Prerelease: true},
	}
	panel := []promotionRelease{
		panelReleaseFixture(300, "v1.22.5", 1),
		panelReleaseFixture(301, "v1.22.6", 1),
		panelReleaseFixture(302, "v1.22.6", 2),
		{ID: 303, TagName: "panel-v1.23.0-bridge.1", Prerelease: true},
	}

	selection, err := selectPromotionCandidates(marshalPromotionFixture(t, official), marshalPromotionFixture(t, plugin), marshalPromotionFixture(t, panel), nil)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Official.Tag != "v7.2.10" || selection.Plugin.Tag != "v1.0.2" || selection.Panel.Tag != "panel-v1.22.6-bridge.2" {
		t.Fatalf("selected tags = %s, %s and %s", selection.Official.Tag, selection.Plugin.Tag, selection.Panel.Tag)
	}
	officialAsset := selection.Official.Assets["archive"]
	if officialAsset.ID != 1011 || officialAsset.Name != "CLIProxyAPI_7.2.10_linux_amd64.tar.gz" ||
		officialAsset.Size != 10101 || officialAsset.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("official asset identity was not locked: %#v", officialAsset)
	}
	pluginAsset := selection.Plugin.Assets["plugin"]
	if pluginAsset.ID != 2011 || pluginAsset.Name != "token-saver-v1.0.2-linux-amd64.so" ||
		pluginAsset.Size != 20101 || pluginAsset.Digest != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("plugin asset identity was not locked: %#v", pluginAsset)
	}
	panelAsset := selection.Panel.Assets["asset"]
	if panelAsset.ID != 3021 || panelAsset.Name != "management.html" ||
		panelAsset.Size != 30201 || panelAsset.Digest != "sha256:"+strings.Repeat("7", 64) {
		t.Fatalf("panel asset identity was not locked: %#v", panelAsset)
	}
}

func TestPromotionSelectionFailsClosed(t *testing.T) {
	validOfficial := officialReleaseFixture(100, "v7.2.10")
	validPlugin := pluginReleaseFixture(200, "v1.0.2")
	validPanel := panelReleaseFixture(300, "v1.22.6", 1)
	tests := []struct {
		name     string
		official promotionRelease
		plugin   promotionRelease
		panel    promotionRelease
		previous *approvedManifest
	}{
		{name: "draft official", official: withReleaseState(validOfficial, true, false), plugin: validPlugin, panel: validPanel},
		{name: "prerelease official", official: withReleaseState(validOfficial, false, true), plugin: validPlugin, panel: validPanel},
		{name: "malformed official tag", official: officialReleaseFixture(100, "7.2.10"), plugin: validPlugin, panel: validPanel},
		{name: "noncanonical official tag", official: officialReleaseFixture(100, "v7.02.10"), plugin: validPlugin, panel: validPanel},
		{name: "no plugin official asset", official: replaceAssetName(validOfficial, "archive", "CLIProxyAPI_7.2.10_linux_amd64_no-plugin.tar.gz"), plugin: validPlugin, panel: validPanel},
		{name: "wrong official architecture", official: replaceAssetName(validOfficial, "archive", "CLIProxyAPI_7.2.10_linux_arm64.tar.gz"), plugin: validPlugin, panel: validPanel},
		{name: "missing official checksum", official: removeAsset(validOfficial, "checksums.txt"), plugin: validPlugin, panel: validPanel},
		{name: "missing official digest", official: clearAssetDigest(validOfficial, "archive"), plugin: validPlugin, panel: validPanel},
		{name: "untagged plugin source", official: validOfficial, plugin: pluginReleaseFixture(200, "main"), panel: validPanel},
		{name: "prerelease plugin", official: validOfficial, plugin: withReleaseState(validPlugin, false, true), panel: validPanel},
		{name: "missing plugin attested asset digest", official: validOfficial, plugin: clearAssetDigest(validPlugin, "plugin"), panel: validPanel},
		{name: "draft panel", official: validOfficial, plugin: validPlugin, panel: withReleaseState(validPanel, true, false)},
		{name: "prerelease panel", official: validOfficial, plugin: validPlugin, panel: withReleaseState(validPanel, false, true)},
		{name: "malformed panel tag", official: validOfficial, plugin: validPlugin, panel: promotionRelease{ID: 300, TagName: "v1.22.6"}},
		{name: "noncanonical panel tag", official: validOfficial, plugin: validPlugin, panel: promotionRelease{ID: 300, TagName: "panel-v01.22.6-bridge.1"}},
		{name: "missing panel asset", official: validOfficial, plugin: validPlugin, panel: removeAsset(validPanel, "management.html")},
		{name: "missing panel manifest", official: validOfficial, plugin: validPlugin, panel: removeAsset(validPanel, "panel-manifest.json")},
		{name: "missing panel checksums", official: validOfficial, plugin: validPlugin, panel: removeAsset(validPanel, "management.html.sha256")},
		{name: "missing panel digest", official: validOfficial, plugin: validPlugin, panel: clearAssetDigest(validPanel, "asset")},
		{
			name:     "official downgrade",
			official: validOfficial,
			plugin:   validPlugin,
			panel:    validPanel,
			previous: approvedManifestFixture("7.2.11", "1.0.2", 3, strings.Repeat("1", 64)),
		},
		{
			name:     "plugin downgrade",
			official: officialReleaseFixture(100, "v7.2.11"),
			plugin:   validPlugin,
			panel:    validPanel,
			previous: approvedManifestFixture("7.2.10", "1.0.3", 3, strings.Repeat("1", 64)),
		},
		{
			name:     "panel downgrade",
			official: validOfficial,
			plugin:   validPlugin,
			panel:    panelReleaseFixture(300, "v1.22.5", 1),
			previous: approvedManifestFixtureWithPanel("7.2.10", "1.0.2", "1.22.6", 1, 3, strings.Repeat("1", 64)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := selectPromotionCandidates(
				marshalPromotionFixture(t, []promotionRelease{tt.official}),
				marshalPromotionFixture(t, []promotionRelease{tt.plugin}),
				marshalPromotionFixture(t, []promotionRelease{tt.panel}),
				tt.previous,
			)
			if err == nil {
				t.Fatal("invalid release selection succeeded")
			}
		})
	}
}

func TestApprovedManifestStrictParserAcceptsCanonicalFixture(t *testing.T) {
	want := approvedManifestFixture("7.2.137", "1.0.2", 4, "")
	raw := marshalApprovedManifest(t, want)
	got, err := validateApprovedManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != want.Fingerprint || got.ChannelGeneration != 4 ||
		got.Official.Asset.ID != 71371 || got.Plugin.Asset.ID != 1011 || got.Panel.Asset.ID != 3011 {
		t.Fatalf("parsed approved manifest lost locked identity: %#v", got)
	}
}

func TestApprovedManifestRejectsAmbiguousOrUnsafeJSON(t *testing.T) {
	valid := string(marshalApprovedManifest(t, approvedManifestFixture("7.2.137", "1.0.2", 4, "")))
	tests := []struct {
		name string
		raw  string
	}{
		{name: "duplicate key", raw: strings.Replace(valid, `"schema_version":2`, `"schema_version":2,"schema_version":2`, 1)},
		{name: "unknown field", raw: strings.Replace(valid, `"verifier_schema":1`, `"verifier_schema":1,"unknown":true`, 1)},
		{name: "noncanonical version", raw: strings.Replace(valid, `"version":"1.0.2"`, `"version":"01.0.2"`, 1)},
		{name: "overflow", raw: strings.Replace(valid, `"channel_generation":4`, `"channel_generation":18446744073709551616`, 1)},
		{name: "control artifact name", raw: strings.Replace(valid, `"name":"token-saver-v1.0.2-linux-amd64.so"`, `"name":"token-saver-\u0001.so"`, 1)},
		{name: "path-like artifact name", raw: strings.Replace(valid, `"name":"token-saver-v1.0.2-linux-amd64.so"`, `"name":"../token-saver.so"`, 1)},
		{name: "wrong official repository", raw: strings.Replace(valid, `"repository":"router-for-me/CLIProxyAPI"`, `"repository":"attacker/CLIProxyAPI"`, 1)},
		{name: "wrong plugin workflow", raw: strings.Replace(valid, `"workflow":".github/workflows/release.yml"`, `"workflow":".github/workflows/other.yml"`, 1)},
		{name: "wrong plugin ref", raw: strings.Replace(valid, `"ref":"refs/tags/v1.0.2"`, `"ref":"refs/heads/main"`, 1)},
		{name: "wrong panel workflow", raw: strings.Replace(valid, `"workflow":".github/workflows/release-panel.yml"`, `"workflow":".github/workflows/other.yml"`, 1)},
		{name: "wrong panel repository", raw: strings.Replace(valid, `"repository":"jinpeng2700-tech/cpa-plugin-token-saver"`, `"repository":"attacker/cpa-plugin-token-saver"`, 1)},
		{name: "malformed panel tag", raw: strings.Replace(valid, `"tag":"panel-v1.22.6-bridge.1"`, `"tag":"v1.22.6"`, 1)},
		{name: "missing attestation", raw: strings.Replace(valid, `"issuer":"https://token.actions.githubusercontent.com"`, `"issuer":""`, 1)},
		{name: "missing checksum identity", raw: strings.Replace(valid, `"name":"checksums.txt"`, `"name":""`, 1)},
		{name: "trailing JSON", raw: valid + `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validateApprovedManifest([]byte(tt.raw)); err == nil {
				t.Fatal("invalid approved manifest succeeded")
			}
		})
	}
}

func TestApprovedManifestSchemaAndTrustPolicyAreClosed(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(readRepositoryFile(t, "deploy/approved-release.schema.json")), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" ||
		schema["additionalProperties"] != false {
		t.Fatal("approved release schema must use draft 2020-12 and reject unknown top-level fields")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("approved release schema has no properties")
	}
	for _, name := range []string{
		"schema_version", "verifier_schema", "channel", "channel_generation", "prior_fingerprint",
		"fingerprint", "official", "plugin", "panel", "compatibility", "approved_attestation",
	} {
		if _, ok := properties[name]; !ok {
			t.Errorf("approved release schema missing %q", name)
		}
	}

	schemaVersionProp, ok := properties["schema_version"].(map[string]any)
	if !ok || schemaVersionProp["const"] != float64(2) {
		t.Fatalf("approved release schema_version = %#v, want const 2", properties["schema_version"])
	}

	panelProp, ok := properties["panel"].(map[string]any)
	if !ok || panelProp["additionalProperties"] != false {
		t.Fatalf("panel property must be object with additionalProperties: false")
	}
	panelProperties, ok := panelProp["properties"].(map[string]any)
	if !ok {
		t.Fatalf("panel property missing properties map")
	}
	for _, field := range []string{
		"repository", "release_id", "tag", "upstream_tag", "upstream_commit",
		"patch_sha256", "asset", "manifest", "attestation",
	} {
		if _, ok := panelProperties[field]; !ok {
			t.Errorf("panel schema missing field %q", field)
		}
	}
	panelRepo, ok := panelProperties["repository"].(map[string]any)
	if !ok || panelRepo["const"] != "jinpeng2700-tech/cpa-plugin-token-saver" {
		t.Errorf("panel repository = %#v, want const jinpeng2700-tech/cpa-plugin-token-saver", panelProperties["repository"])
	}
	panelTag, ok := panelProperties["tag"].(map[string]any)
	if !ok || panelTag["pattern"] != "^panel-v[0-9]+\\.[0-9]+\\.[0-9]+-bridge\\.[1-9][0-9]*$" {
		t.Errorf("panel tag pattern = %#v", panelProperties["tag"])
	}

	var policy struct {
		SchemaVersion  uint32 `json:"schema_version"`
		VerifierSchema uint32 `json:"verifier_schema"`
		Official       struct {
			Repository string `json:"repository"`
			Provenance string `json:"provenance"`
		} `json:"official"`
		Plugin   approvedAttestation `json:"plugin_attestation"`
		Panel    approvedAttestation `json:"panel_attestation"`
		Approved approvedAttestation `json:"approved_attestation"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, "deploy/trust-policy.json")), &policy); err != nil {
		t.Fatal(err)
	}
	if policy.SchemaVersion != 1 || policy.VerifierSchema != 1 ||
		policy.Official.Repository != "router-for-me/CLIProxyAPI" ||
		policy.Official.Provenance != "official-checksum-only" ||
		policy.Plugin.Repository != "jinpeng2700-tech/cpa-plugin-token-saver" ||
		policy.Plugin.Workflow != ".github/workflows/release.yml" ||
		policy.Panel.Repository != "jinpeng2700-tech/cpa-plugin-token-saver" ||
		policy.Panel.Workflow != ".github/workflows/release-panel.yml" ||
		policy.Panel.Ref != "refs/heads/main" ||
		policy.Approved.Workflow != ".github/workflows/promote-cliproxyapi.yml" ||
		policy.Approved.Ref != "refs/heads/main" {
		t.Fatalf("trust policy identity is incomplete: %#v", policy)
	}

	var channel map[string]any
	if err := json.Unmarshal([]byte(readRepositoryFile(t, "deploy/channel.json")), &channel); err != nil {
		t.Fatal(err)
	}
	if channel["schema_version"] != float64(1) || channel["channel"] != "stable" ||
		channel["channel_generation"] != float64(0) || channel["fingerprint"] != nil ||
		channel["approved_tag"] != nil || channel["manifest_asset"] != "approved-release.json" {
		t.Fatalf("channel genesis is invalid: %#v", channel)
	}
}

func TestPromotionPanelOnlyChangeIncrementsGenerationAndChangesFingerprint(t *testing.T) {
	locked1 := promotionLockedFixtureWithPanel(t, "7.2.138", "1.0.2", "1.22.6", 1)
	locked2 := promotionLockedFixtureWithPanel(t, "7.2.138", "1.0.2", "1.22.6", 2)
	pluginReport := validPromotionPluginReport()
	coreReport := promotionProbeReport{SchemaVersion: 1, Compatible: true, Code: "ok"}
	previous := approvedManifestFixture("7.2.137", "1.0.2", 4, "")

	result1, err := buildPromotionResult(locked1, pluginReport, coreReport, previous, strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	if !result1.Publish || result1.Manifest.ChannelGeneration != 5 {
		t.Fatalf("result1 failed: %#v", result1)
	}

	dup, err := buildPromotionResult(locked1, pluginReport, coreReport, &result1.Manifest, strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	if dup.Publish || dup.Manifest.ChannelGeneration != 5 || dup.Manifest.Fingerprint != result1.Manifest.Fingerprint {
		t.Fatalf("identical panel did not converge: %#v", dup)
	}

	result2, err := buildPromotionResult(locked2, pluginReport, coreReport, &result1.Manifest, strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	if !result2.Publish || result2.Manifest.ChannelGeneration != 6 ||
		result2.Manifest.PriorFingerprint == nil || *result2.Manifest.PriorFingerprint != result1.Manifest.Fingerprint ||
		result2.Manifest.Fingerprint == result1.Manifest.Fingerprint {
		t.Fatalf("panel-only change did not increment generation or change fingerprint: %#v", result2)
	}
}

func TestPromotionTransitionFromSchemaV1Lineage(t *testing.T) {
	locked := promotionLockedFixture(t, "7.2.138", "1.0.2")
	pluginReport := validPromotionPluginReport()
	coreReport := promotionProbeReport{SchemaVersion: 1, Compatible: true, Code: "ok"}
	previousV1 := approvedManifestV1Fixture("7.2.137", "1.0.2", 4, "")

	result, err := buildPromotionResult(locked, pluginReport, coreReport, previousV1, strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.SchemaVersion != 2 || !result.Publish || result.Manifest.ChannelGeneration != 5 ||
		result.Manifest.PriorFingerprint == nil || *result.Manifest.PriorFingerprint != previousV1.Fingerprint {
		t.Fatalf("transition from schema v1 predecessor is invalid: %#v", result)
	}
	if _, err := validateApprovedManifest(marshalApprovedManifest(t, &result.Manifest)); err != nil {
		t.Fatalf("output manifest from v1 transition is not valid schema v2: %v", err)
	}
}

func TestPromotionLocksBytesBeforeCompatibility(t *testing.T) {
	root := t.TempDir()
	paths := promotionPaths{
		OfficialArchive:    filepath.Join(root, "official.tar.gz"),
		OfficialChecksums:  filepath.Join(root, "checksums.txt"),
		OfficialBinary:     filepath.Join(root, "cli-proxy-api"),
		Plugin:             filepath.Join(root, "token-saver.so"),
		Probe:              filepath.Join(root, "compat-probe"),
		PluginMetadata:     filepath.Join(root, "release-metadata.json"),
		PluginChecksums:    filepath.Join(root, "SHA256SUMS"),
		PluginSourceCommit: strings.Repeat("d", 40),
		PanelAsset:         filepath.Join(root, "management.html"),
		PanelChecksums:     filepath.Join(root, "management.html.sha256"),
		PanelManifest:      filepath.Join(root, "panel-manifest.json"),
		PanelSourceCommit:  strings.Repeat("8", 40),
	}
	writePromotionFile(t, paths.PanelAsset, "panel html")
	writePromotionFile(t, paths.OfficialArchive, "official archive")
	writePromotionFile(t, paths.OfficialBinary, "official binary")
	writePromotionFile(t, paths.Plugin, "plugin bytes")
	writePromotionFile(t, paths.Probe, "probe bytes")

	selection, err := selectPromotionCandidates(
		marshalPromotionFixture(t, []promotionRelease{officialReleaseFixture(7137, "v7.2.137")}),
		marshalPromotionFixture(t, []promotionRelease{pluginReleaseFixture(101, "v1.0.2")}),
		marshalPromotionFixture(t, []promotionRelease{panelReleaseFixture(301, "v1.22.6", 1)}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	setLockedAsset(t, &selection.Official, "archive", paths.OfficialArchive)
	setLockedAsset(t, &selection.Plugin, "plugin", paths.Plugin)
	setLockedAsset(t, &selection.Plugin, "probe", paths.Probe)
	setLockedAsset(t, &selection.Panel, "asset", paths.PanelAsset)
	writePromotionFile(t, paths.OfficialChecksums, fileSHA256(t, paths.OfficialArchive)+"  "+selection.Official.Assets["archive"].Name+"\n")
	setLockedAsset(t, &selection.Official, "checksums", paths.OfficialChecksums)
	writePromotionFile(t, paths.PluginMetadata, `{"version":"1.0.2","tag":"v1.0.2","source_commit":"`+strings.Repeat("d", 40)+`","platform":"linux-amd64","abi":1,"rpc":3,"glibc_max":"2.32"}`)
	setLockedAsset(t, &selection.Plugin, "metadata", paths.PluginMetadata)
	writePromotionFile(t, paths.PluginChecksums,
		fileSHA256(t, paths.Probe)+"  "+selection.Plugin.Assets["probe"].Name+"\n"+
			fileSHA256(t, paths.Plugin)+"  "+selection.Plugin.Assets["plugin"].Name+"\n")
	setLockedAsset(t, &selection.Plugin, "checksums", paths.PluginChecksums)
	writePromotionFile(t, paths.PanelChecksums, fileSHA256(t, paths.PanelAsset)+"  "+selection.Panel.Assets["asset"].Name+"\n")
	setLockedAsset(t, &selection.Panel, "checksums", paths.PanelChecksums)
	panelManifestJSON := fmt.Sprintf(`{"schema_version":1,"schema_id":"cliproxyapi-patched-management-release/v1","upstream_repository":"https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git","upstream_tag":"v1.22.6","upstream_commit":"%s","patch_file":"0001-plugin-management-bridge.patch","patch_sha256":"%s","asset":{"name":"management.html","size":%d,"sha256":"%s"}}`,
		strings.Repeat("7", 40), strings.Repeat("8", 64), len("panel html"), fileSHA256(t, paths.PanelAsset))
	writePromotionFile(t, paths.PanelManifest, panelManifestJSON+"\n")
	setLockedAsset(t, &selection.Panel, "manifest", paths.PanelManifest)

	locked, err := lockPromotionArtifacts(selection, paths)
	if err != nil {
		t.Fatal(err)
	}
	if locked.OfficialArchiveSHA256 != fileSHA256(t, paths.OfficialArchive) ||
		locked.PluginSHA256 != fileSHA256(t, paths.Plugin) ||
		locked.PluginSourceCommit != strings.Repeat("d", 40) {
		t.Fatalf("locked identity is incomplete: %#v", locked)
	}
	writePromotionFile(t, paths.PanelAsset, "tampered")
	if _, err := lockPromotionArtifacts(selection, paths); err == nil {
		t.Fatal("tampered candidate panel bytes were accepted")
	}
}

func TestPromotionGenerationIsMonotonicAndDuplicateConverges(t *testing.T) {
	locked := promotionLockedFixture(t, "7.2.138", "1.0.2")
	pluginReport := validPromotionPluginReport()
	coreReport := promotionProbeReport{SchemaVersion: 1, Compatible: true, Code: "ok"}
	previous := approvedManifestFixture("7.2.137", "1.0.2", 4, "")

	result, err := buildPromotionResult(locked, pluginReport, coreReport, previous, strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Publish || result.Manifest.ChannelGeneration != 5 ||
		result.Manifest.PriorFingerprint == nil || *result.Manifest.PriorFingerprint != previous.Fingerprint {
		t.Fatalf("new promotion lineage is invalid: %#v", result)
	}
	if _, err := validateApprovedManifest(marshalApprovedManifest(t, &result.Manifest)); err != nil {
		t.Fatalf("generated manifest is invalid: %v", err)
	}

	duplicate, err := buildPromotionResult(locked, pluginReport, coreReport, &result.Manifest, strings.Repeat("2", 40))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Publish || duplicate.Manifest.ChannelGeneration != result.Manifest.ChannelGeneration ||
		duplicate.Manifest.Fingerprint != result.Manifest.Fingerprint {
		t.Fatalf("duplicate promotion did not converge: %#v", duplicate)
	}
}

func TestPromotionCompatibilityFailureProducesNoManifest(t *testing.T) {
	locked := promotionLockedFixture(t, "7.2.138", "1.0.2")
	failed := validPromotionPluginReport()
	failed.Compatible = false
	failed.Code = "self_test_failed"
	if _, err := buildPromotionResult(locked, failed, promotionProbeReport{SchemaVersion: 1, Compatible: true, Code: "ok"}, nil, strings.Repeat("2", 40)); err == nil {
		t.Fatal("compatibility failure produced an approved manifest")
	}
}

func TestPromotionWorkflowLocksExactAssetsRunsGateAndPublishesImmutably(t *testing.T) {
	workflow := readPromotionWorkflow(t)
	discover := requireReleaseJob(t, workflow, "discover")
	compatibility := requireReleaseJob(t, workflow, "compatibility")
	publish := requireReleaseJob(t, workflow, "publish")
	if !jobNeeds(compatibility, "discover") || !jobNeeds(publish, "compatibility") {
		t.Fatal("promotion job ordering is incomplete")
	}
	all := joinedRun(discover) + "\n" + joinedRun(compatibility) + "\n" + joinedRun(publish)
	for _, want := range []string{
		"--paginate", "PROMOTION_COMMAND=select", "/releases/assets/", "PROMOTION_COMMAND=lock",
		"gh attestation verify", "-mode core-only", "TestRealCandidate", "PROMOTION_COMMAND=manifest",
		"gh release create", "approved-release.json", "channel.json",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("promotion workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"/releases/latest", "gh release upload", "--clobber", "_no-plugin"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("promotion workflow contains forbidden %q", forbidden)
		}
	}
	if strings.Index(all, "PROMOTION_COMMAND=lock") >= strings.Index(all, `"$probe" -candidate`) {
		t.Fatal("candidate identities must be locked before compatibility execution")
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			if !regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`).MatchString(step.Uses) {
				t.Errorf("promotion action is not pinned: %s", step.Uses)
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") && workflowValue(step.With, "persist-credentials") != "false" {
				t.Fatal("promotion checkout persists credentials")
			}
		}
	}
}

func TestPromotionCommand(t *testing.T) {
	command := os.Getenv("PROMOTION_COMMAND")
	if command == "" {
		t.Skip("PROMOTION_COMMAND is not set")
	}
	switch command {
	case "select":
		official := readPromotionCommandFile(t, "OFFICIAL_RELEASES")
		plugin := readPromotionCommandFile(t, "PLUGIN_RELEASES")
		previous := newestApprovedManifest(t, os.Getenv("PREVIOUS_DIR"))
		panelRaw := plugin
		if os.Getenv("PANEL_RELEASES") != "" {
			panelRaw = readPromotionCommandFile(t, "PANEL_RELEASES")
		}
		selection, err := selectPromotionCandidates(official, plugin, panelRaw, previous)
		if err != nil {
			t.Fatal(err)
		}
		writePromotionJSON(t, os.Getenv("SELECTION_OUTPUT"), selection)
		if previous == nil {
			writePromotionFile(t, os.Getenv("PREVIOUS_OUTPUT"), "null\n")
		} else {
			writePromotionJSON(t, os.Getenv("PREVIOUS_OUTPUT"), previous)
		}
	case "lock":
		var selection promotionSelection
		readPromotionJSON(t, os.Getenv("SELECTION_INPUT"), &selection)
		locked, err := lockPromotionArtifacts(selection, promotionPaths{
			OfficialArchive:    os.Getenv("OFFICIAL_ARCHIVE"),
			OfficialChecksums:  os.Getenv("OFFICIAL_CHECKSUMS"),
			OfficialBinary:     os.Getenv("OFFICIAL_BINARY"),
			Plugin:             os.Getenv("PLUGIN_ASSET"),
			Probe:              os.Getenv("PROBE_ASSET"),
			PluginMetadata:     os.Getenv("PLUGIN_METADATA"),
			PluginChecksums:    os.Getenv("PLUGIN_CHECKSUMS"),
			PluginSourceCommit: os.Getenv("PLUGIN_SOURCE_COMMIT"),
			PanelAsset:         os.Getenv("PANEL_ASSET"),
			PanelChecksums:     os.Getenv("PANEL_CHECKSUMS"),
			PanelManifest:      os.Getenv("PANEL_MANIFEST"),
			PanelSourceCommit:  os.Getenv("PANEL_SOURCE_COMMIT"),
		})
		if err != nil {
			t.Fatal(err)
		}
		writePromotionJSON(t, os.Getenv("LOCKED_OUTPUT"), locked)
	case "manifest":
		var locked promotionLocked
		var pluginReport, coreReport promotionProbeReport
		readPromotionJSON(t, os.Getenv("LOCKED_INPUT"), &locked)
		readPromotionJSON(t, os.Getenv("PLUGIN_REPORT"), &pluginReport)
		readPromotionJSON(t, os.Getenv("CORE_REPORT"), &coreReport)
		previous := readOptionalApprovedManifest(t, os.Getenv("PREVIOUS_INPUT"))
		result, err := buildPromotionResult(locked, pluginReport, coreReport, previous, os.Getenv("SOURCE_COMMIT"))
		if err != nil {
			t.Fatal(err)
		}
		writePromotionJSON(t, os.Getenv("MANIFEST_OUTPUT"), result.Manifest)
		writePromotionJSON(t, os.Getenv("CHANNEL_OUTPUT"), result.Channel)
		writePromotionJSON(t, os.Getenv("RESULT_OUTPUT"), map[string]any{"publish": result.Publish, "tag": result.Tag})
	case "validate":
		if _, err := validateApprovedManifest(readPromotionCommandFile(t, "MANIFEST_INPUT")); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown PROMOTION_COMMAND %q", command)
	}
}

func marshalPromotionFixture(t *testing.T, releases []promotionRelease) []byte {
	t.Helper()
	raw, err := json.Marshal([][]promotionRelease{releases})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func officialReleaseFixture(id uint64, tag string) promotionRelease {
	version := strings.TrimPrefix(tag, "v")
	return promotionRelease{
		ID: id, TagName: tag,
		Assets: []promotionAsset{
			{ID: id*10 + 1, Name: "CLIProxyAPI_" + version + "_linux_amd64.tar.gz", Size: id*100 + 1, Digest: "sha256:" + strings.Repeat("a", 64)},
			{ID: id*10 + 2, Name: "checksums.txt", Size: id*100 + 2, Digest: "sha256:" + strings.Repeat("b", 64)},
		},
	}
}

func pluginReleaseFixture(id uint64, tag string) promotionRelease {
	version := strings.TrimPrefix(tag, "v")
	return promotionRelease{
		ID: id, TagName: tag,
		Assets: []promotionAsset{
			{ID: id*10 + 1, Name: "token-saver-v" + version + "-linux-amd64.so", Size: id*100 + 1, Digest: "sha256:" + strings.Repeat("c", 64)},
			{ID: id*10 + 2, Name: "compat-probe-v" + version + "-linux-amd64", Size: id*100 + 2, Digest: "sha256:" + strings.Repeat("d", 64)},
			{ID: id*10 + 3, Name: "release-metadata.json", Size: id*100 + 3, Digest: "sha256:" + strings.Repeat("e", 64)},
			{ID: id*10 + 4, Name: "SHA256SUMS", Size: id*100 + 4, Digest: "sha256:" + strings.Repeat("f", 64)},
		},
	}
}

func panelReleaseFixture(id uint64, upstreamTag string, bridgeRev uint64) promotionRelease {
	upstream := strings.TrimPrefix(upstreamTag, "v")
	tag := fmt.Sprintf("panel-v%s-bridge.%d", upstream, bridgeRev)
	return promotionRelease{
		ID: id, TagName: tag,
		Assets: []promotionAsset{
			{ID: id*10 + 1, Name: "management.html", Size: id*100 + 1, Digest: "sha256:" + strings.Repeat("7", 64)},
			{ID: id*10 + 2, Name: "management.html.sha256", Size: id*100 + 2, Digest: "sha256:" + strings.Repeat("8", 64)},
			{ID: id*10 + 3, Name: "panel-manifest.json", Size: id*100 + 3, Digest: "sha256:" + strings.Repeat("9", 64)},
		},
	}
}

func withReleaseState(release promotionRelease, draft, prerelease bool) promotionRelease {
	cloned := release
	cloned.Draft = draft
	cloned.Prerelease = prerelease
	return cloned
}

func replaceAssetName(release promotionRelease, key, name string) promotionRelease {
	cloned := release
	cloned.Assets = append([]promotionAsset(nil), release.Assets...)
	index := promotionAssetIndex(cloned, key)
	cloned.Assets[index].Name = name
	return cloned
}

func removeAsset(release promotionRelease, name string) promotionRelease {
	cloned := release
	cloned.Assets = append([]promotionAsset(nil), release.Assets...)
	for index, asset := range cloned.Assets {
		if asset.Name == name {
			cloned.Assets = append(cloned.Assets[:index], cloned.Assets[index+1:]...)
			return cloned
		}
	}
	return cloned
}

func clearAssetDigest(release promotionRelease, key string) promotionRelease {
	cloned := release
	cloned.Assets = append([]promotionAsset(nil), release.Assets...)
	index := promotionAssetIndex(cloned, key)
	cloned.Assets[index].Digest = ""
	return cloned
}

func promotionAssetIndex(release promotionRelease, key string) int {
	for index, asset := range release.Assets {
		switch key {
		case "archive":
			if strings.HasPrefix(asset.Name, "CLIProxyAPI_") {
				return index
			}
		case "plugin":
			if strings.HasPrefix(asset.Name, "token-saver-") {
				return index
			}
		case "asset":
			if asset.Name == "management.html" {
				return index
			}
		}
	}
	panic("fixture asset not found")
}

func approvedManifestFixture(officialVersion, pluginVersion string, generation uint64, fingerprint string) *approvedManifest {
	return approvedManifestFixtureWithPanel(officialVersion, pluginVersion, "1.22.6", 1, generation, fingerprint)
}

func approvedManifestFixtureWithPanel(officialVersion, pluginVersion, panelUpstreamVersion string, bridgeRev uint64, generation uint64, fingerprint string) *approvedManifest {
	prior := "sha256:" + strings.Repeat("0", 64)
	panelTag := fmt.Sprintf("panel-v%s-bridge.%d", panelUpstreamVersion, bridgeRev)
	manifest := &approvedManifest{
		SchemaVersion:     2,
		VerifierSchema:    1,
		Channel:           "stable",
		ChannelGeneration: generation,
		PriorFingerprint:  &prior,
		Official: approvedOfficial{
			Repository: "router-for-me/CLIProxyAPI",
			ReleaseID:  7137,
			Tag:        "v" + officialVersion,
			Version:    officialVersion,
			Asset: approvedAsset{
				Name: "CLIProxyAPI_" + officialVersion + "_linux_amd64.tar.gz",
				ID:   71371, Size: 713701, SHA256: strings.Repeat("a", 64),
			},
			Checksums: approvedAsset{
				Name: "checksums.txt",
				ID:   71372, Size: 713702, SHA256: strings.Repeat("b", 64),
			},
			BinarySHA256: strings.Repeat("c", 64),
			Provenance:   "official-checksum-only",
		},
		Plugin: approvedPlugin{
			Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
			ReleaseID:    101,
			Tag:          "v" + pluginVersion,
			Version:      pluginVersion,
			SourceCommit: strings.Repeat("d", 40),
			Asset: approvedAsset{
				Name: "token-saver-v" + pluginVersion + "-linux-amd64.so",
				ID:   1011, Size: 10101, SHA256: strings.Repeat("e", 64),
			},
			ProbeAsset: approvedAsset{
				Name: "compat-probe-v" + pluginVersion + "-linux-amd64",
				ID:   1012, Size: 10102, SHA256: strings.Repeat("f", 64),
			},
			Attestation: approvedAttestation{
				Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
				Workflow:     ".github/workflows/release.yml",
				Ref:          "refs/tags/v" + pluginVersion,
				SourceCommit: strings.Repeat("d", 40),
				Issuer:       "https://token.actions.githubusercontent.com",
			},
		},
		Panel: approvedPanel{
			Repository:     "jinpeng2700-tech/cpa-plugin-token-saver",
			ReleaseID:      301,
			Tag:            panelTag,
			UpstreamTag:    "v" + panelUpstreamVersion,
			UpstreamCommit: strings.Repeat("8", 40),
			PatchSHA256:    strings.Repeat("9", 64),
			Asset: approvedAsset{
				Name:   "management.html",
				ID:     3011,
				Size:   30101,
				SHA256: strings.Repeat("7", 64),
			},
			Manifest: approvedAsset{
				Name:   "panel-manifest.json",
				ID:     3013,
				Size:   30103,
				SHA256: strings.Repeat("6", 64),
			},
			Attestation: approvedAttestation{
				Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
				Workflow:     ".github/workflows/release-panel.yml",
				Ref:          "refs/heads/main",
				SourceCommit: strings.Repeat("8", 40),
				Issuer:       "https://token.actions.githubusercontent.com",
			},
		},
		Compatibility: approvedCompatibility{
			SchemaVersion: 1,
			Plugin:        true, CoreOnly: true,
			ConfigGeneration: 8,
			ConfigDigest:     strings.Repeat("1", 64),
			Scenarios:        []string{"all-off", "rtk", "headroom-rewrite", "headroom-timeout", "caveman", "ponytail", "fixed-order"},
		},
		ApprovedAttestation: approvedAttestation{
			Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
			Workflow:     ".github/workflows/promote-cliproxyapi.yml",
			Ref:          "refs/heads/main",
			SourceCommit: strings.Repeat("2", 40),
			Issuer:       "https://token.actions.githubusercontent.com",
		},
	}
	if generation == 1 {
		manifest.PriorFingerprint = nil
	}
	if fingerprint == "" {
		manifest.Fingerprint = computeApprovedFingerprint(*manifest)
	} else {
		manifest.Fingerprint = "sha256:" + fingerprint
	}
	return manifest
}

func approvedManifestV1Fixture(officialVersion, pluginVersion string, generation uint64, fingerprint string) *approvedManifest {
	prior := "sha256:" + strings.Repeat("0", 64)
	v1 := approvedManifestV1{
		SchemaVersion:     1,
		VerifierSchema:    1,
		Channel:           "stable",
		ChannelGeneration: generation,
		PriorFingerprint:  &prior,
		Official: approvedOfficial{
			Repository: "router-for-me/CLIProxyAPI",
			ReleaseID:  7137,
			Tag:        "v" + officialVersion,
			Version:    officialVersion,
			Asset: approvedAsset{
				Name: "CLIProxyAPI_" + officialVersion + "_linux_amd64.tar.gz",
				ID:   71371, Size: 713701, SHA256: strings.Repeat("a", 64),
			},
			Checksums: approvedAsset{
				Name: "checksums.txt",
				ID:   71372, Size: 713702, SHA256: strings.Repeat("b", 64),
			},
			BinarySHA256: strings.Repeat("c", 64),
			Provenance:   "official-checksum-only",
		},
		Plugin: approvedPlugin{
			Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
			ReleaseID:    101,
			Tag:          "v" + pluginVersion,
			Version:      pluginVersion,
			SourceCommit: strings.Repeat("d", 40),
			Asset: approvedAsset{
				Name: "token-saver-v" + pluginVersion + "-linux-amd64.so",
				ID:   1011, Size: 10101, SHA256: strings.Repeat("e", 64),
			},
			ProbeAsset: approvedAsset{
				Name: "compat-probe-v" + pluginVersion + "-linux-amd64",
				ID:   1012, Size: 10102, SHA256: strings.Repeat("f", 64),
			},
			Attestation: approvedAttestation{
				Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
				Workflow:     ".github/workflows/release.yml",
				Ref:          "refs/tags/v" + pluginVersion,
				SourceCommit: strings.Repeat("d", 40),
				Issuer:       "https://token.actions.githubusercontent.com",
			},
		},
		Compatibility: approvedCompatibility{
			SchemaVersion: 1,
			Plugin:        true, CoreOnly: true,
			ConfigGeneration: 8,
			ConfigDigest:     strings.Repeat("1", 64),
			Scenarios:        []string{"all-off", "rtk", "headroom-rewrite", "headroom-timeout", "caveman", "ponytail", "fixed-order"},
		},
		ApprovedAttestation: approvedAttestation{
			Repository:   "jinpeng2700-tech/cpa-plugin-token-saver",
			Workflow:     ".github/workflows/promote-cliproxyapi.yml",
			Ref:          "refs/heads/main",
			SourceCommit: strings.Repeat("2", 40),
			Issuer:       "https://token.actions.githubusercontent.com",
		},
	}
	if generation == 1 {
		v1.PriorFingerprint = nil
	}
	if fingerprint == "" {
		material := struct {
			SchemaVersion       uint32                `json:"schema_version"`
			VerifierSchema      uint32                `json:"verifier_schema"`
			Channel             string                `json:"channel"`
			Official            approvedOfficial      `json:"official"`
			Plugin              approvedPlugin        `json:"plugin"`
			Compatibility       approvedCompatibility `json:"compatibility"`
			ApprovedAttestation approvedAttestation   `json:"approved_attestation"`
		}{
			SchemaVersion:       v1.SchemaVersion,
			VerifierSchema:      v1.VerifierSchema,
			Channel:             v1.Channel,
			Official:            v1.Official,
			Plugin:              v1.Plugin,
			Compatibility:       v1.Compatibility,
			ApprovedAttestation: v1.ApprovedAttestation,
		}
		raw, _ := json.Marshal(material)
		sum := sha256.Sum256(raw)
		v1.Fingerprint = fmt.Sprintf("sha256:%x", sum)
	} else {
		v1.Fingerprint = "sha256:" + fingerprint
	}
	return &approvedManifest{
		SchemaVersion:       v1.SchemaVersion,
		VerifierSchema:      v1.VerifierSchema,
		Channel:             v1.Channel,
		ChannelGeneration:   v1.ChannelGeneration,
		PriorFingerprint:    v1.PriorFingerprint,
		Fingerprint:         v1.Fingerprint,
		Official:            v1.Official,
		Plugin:              v1.Plugin,
		Compatibility:       v1.Compatibility,
		ApprovedAttestation: v1.ApprovedAttestation,
	}
}

func marshalApprovedManifest(t *testing.T, manifest *approvedManifest) []byte {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writePromotionFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum)
}

func setLockedAsset(t *testing.T, release *selectedRelease, key, name string) {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	asset := release.Assets[key]
	asset.Size = uint64(info.Size())
	asset.Digest = "sha256:" + fileSHA256(t, name)
	release.Assets[key] = asset
}

func promotionLockedFixture(t *testing.T, officialVersion, pluginVersion string) promotionLocked {
	return promotionLockedFixtureWithPanel(t, officialVersion, pluginVersion, "1.22.6", 1)
}

func promotionLockedFixtureWithPanel(t *testing.T, officialVersion, pluginVersion, panelUpstreamVersion string, bridgeRev uint64) promotionLocked {
	t.Helper()
	selection, err := selectPromotionCandidates(
		marshalPromotionFixture(t, []promotionRelease{officialReleaseFixture(7138, "v"+officialVersion)}),
		marshalPromotionFixture(t, []promotionRelease{pluginReleaseFixture(101, "v"+pluginVersion)}),
		marshalPromotionFixture(t, []promotionRelease{panelReleaseFixture(301, panelUpstreamVersion, bridgeRev)}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return promotionLocked{
		Selection:               selection,
		OfficialArchiveSHA256:   strings.Repeat("a", 64),
		OfficialChecksumsSHA256: strings.Repeat("b", 64),
		OfficialBinarySHA256:    strings.Repeat("c", 64),
		PluginSHA256:            strings.Repeat("e", 64),
		ProbeSHA256:             strings.Repeat("f", 64),
		PluginSourceCommit:      strings.Repeat("d", 40),
		PanelAssetSHA256:        strings.Repeat("7", 64),
		PanelManifestSHA256:     strings.Repeat("6", 64),
		PanelSourceCommit:       strings.Repeat("8", 40),
		PanelUpstreamTag:        "v" + panelUpstreamVersion,
		PanelUpstreamCommit:     strings.Repeat("8", 40),
		PanelPatchSHA256:        strings.Repeat("9", 64),
	}
}

func validPromotionPluginReport() promotionProbeReport {
	return promotionProbeReport{
		SchemaVersion:    1,
		Compatible:       true,
		Code:             "ok",
		ConfigGeneration: 8,
		ConfigDigest:     strings.Repeat("1", 64),
		Scenarios:        []string{"all-off", "rtk", "headroom-rewrite", "headroom-timeout", "caveman", "ponytail", "fixed-order"},
	}
}

func readPromotionCommandFile(t *testing.T, environment string) []byte {
	t.Helper()
	name := os.Getenv(environment)
	if name == "" {
		t.Fatalf("%s is not set", environment)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readPromotionJSON(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeStrictPromotionJSON(raw, target); err != nil {
		t.Fatal(err)
	}
}

func writePromotionJSON(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writePromotionFile(t, name, string(raw)+"\n")
}

func readOptionalApprovedManifest(t *testing.T, name string) *approvedManifest {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	manifest, err := validatePreviousApprovedManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &manifest
}

func newestApprovedManifest(t *testing.T, directory string) *approvedManifest {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var newest *approvedManifest
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := validatePreviousApprovedManifest(raw)
		if err != nil {
			t.Fatalf("invalid prior approved manifest %s: %v", entry.Name(), err)
		}
		if newest == nil || manifest.ChannelGeneration > newest.ChannelGeneration {
			copy := manifest
			newest = &copy
		} else if manifest.ChannelGeneration == newest.ChannelGeneration && manifest.Fingerprint != newest.Fingerprint {
			t.Fatal("ambiguous approved channel generation")
		}
	}
	return newest
}
