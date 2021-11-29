package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/filecoin-project/lotus/chain/types"
	cliutil "github.com/filecoin-project/lotus/cli/util"
	"github.com/ipfs/go-cid"
	"github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"
	"io/ioutil"
	"sort"
	"time"
)

var ExportCmd = &cli.Command{
	Name:  "export",
	Usage: "Recovery sector tools",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "miner",
			Usage:    "Filecoin Miner. Such as: f01000",
			Required: true,
		},
		&cli.IntSliceFlag{
			Name:     "sector",
			Usage:    "Sector number to be recovered. Such as: 0",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {

		start := time.Now()

		maddr, err := address.NewFromString(cctx.String("miner"))
		if err != nil {
			return xerrors.Errorf("Getting NewFromString err:", err)
		}

		fullNodeApi, closer, err := cliutil.GetFullNodeAPI(cctx)
		if err != nil {
			return xerrors.Errorf("Getting FullNodeAPI err:", err)
		}
		defer closer()

		//Sector size
		mi, err := fullNodeApi.StateMinerInfo(context.Background(), maddr, types.EmptyTSK)
		if err != nil {
			return xerrors.Errorf("Getting StateMinerInfo err:", err)
		}

		sectorInfos := make(SectorInfos, 0)
		failtSectors := make([]uint64, 0)
		for _, sector := range cctx.IntSlice("sector") {
			si, err := fullNodeApi.StateSectorGetInfo(context.Background(), maddr, abi.SectorNumber(sector), types.EmptyTSK)
			if err != nil {
				log.Errorf("Sector (%d), StateSectorGetInfo error: %v", sector, err)
				failtSectors = append(failtSectors, uint64(sector))
				continue
			}

			if si == nil {
				//ProveCommit not submitted
				preCommitInfo, err := fullNodeApi.StateSectorPreCommitInfo(context.Background(), maddr, abi.SectorNumber(sector), types.EmptyTSK)
				if err != nil {
					log.Errorf("Sector (%d), StateSectorPreCommitInfo error: %v", sector, err)
					failtSectors = append(failtSectors, uint64(sector))
					continue
				}
				sectorInfos = append(sectorInfos, &SectorInfo{
					SectorNumber: abi.SectorNumber(sector),
					SealProof:    preCommitInfo.Info.SealProof,
					Activation:   preCommitInfo.PreCommitEpoch,
					SealedCID:    preCommitInfo.Info.SealedCID,
				})
				continue
			}

			sectorInfos = append(sectorInfos, &SectorInfo{
				SectorNumber: abi.SectorNumber(sector),
				SealProof:    si.SealProof,
				Activation:   si.Activation,
				SealedCID:    si.SealedCID,
			})
		}

		//sort by sectorInfo.Activation
		//walk back from the execTs instead of HEAD, to save time.
		sort.Sort(sectorInfos)

		buf := new(bytes.Buffer)
		if err := maddr.MarshalCBOR(buf); err != nil {
			return xerrors.Errorf("Address MarshalCBOR err:", err)
		}

		tsk := types.EmptyTSK
		for _, sectorInfo := range sectorInfos {
			ts, err := fullNodeApi.ChainGetTipSetByHeight(context.Background(), sectorInfo.Activation, tsk)
			tsk = ts.Key()

			ticket, err := fullNodeApi.StateGetRandomnessFromTickets(context.Background(), crypto.DomainSeparationTag_SealRandomness, sectorInfo.Activation, buf.Bytes(), tsk)
			if err != nil {
				log.Errorf("Sector (%d), Getting Randomness  error: %v", sectorInfo.SectorNumber, err)
				failtSectors = append(failtSectors, uint64(sectorInfo.SectorNumber))
				continue
			}
			sectorInfo.Ticket = ticket
		}

		output := &RecoveryParams{
			Miner:       maddr,
			SectorSize:  mi.SectorSize,
			SectorInfos: sectorInfos,
		}
		out, err := json.MarshalIndent(output, "", "\t")
		if err != nil {
			return err
		}

		of, err := homedir.Expand("sectors-recovery-" + maddr.String() + ".json")
		if err != nil {
			return err
		}

		if err := ioutil.WriteFile(of, out, 0644); err != nil {
			return err
		}

		end := time.Now()
		fmt.Println("export", len(sectorInfos), "sectors, failt sectors:", failtSectors, ", elapsed:", end.Sub(start))

		return nil
	},
}

type RecoveryParams struct {
	Miner       address.Address
	SectorSize  abi.SectorSize
	SectorInfos SectorInfos
}

type SectorInfo struct {
	SectorNumber abi.SectorNumber
	Activation   abi.ChainEpoch
	Ticket       abi.Randomness
	SealProof    abi.RegisteredSealProof
	SealedCID    cid.Cid
}

type SectorInfos []*SectorInfo

func (t SectorInfos) Len() int { return len(t) }

func (t SectorInfos) Swap(i, j int) { t[i], t[j] = t[j], t[i] }

func (t SectorInfos) Less(i, j int) bool {
	if t[i].Activation != t[j].Activation {
		return t[i].Activation > t[j].Activation
	}

	return t[i].SectorNumber < t[j].SectorNumber
}
