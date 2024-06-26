// The MIT License (MIT)
//
// Copyright (c) 2017-2020 Uber Technologies Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package shardscanner

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.uber.org/cadence/testsuite"
	"go.uber.org/cadence/workflow"

	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/reconciliation/invariant"
	"github.com/uber/cadence/common/reconciliation/store"
)

type workflowsSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
	env *testsuite.TestWorkflowEnvironment
}

func TestWorkflowsSuite(t *testing.T) {
	suite.Run(t, new(workflowsSuite))
}

func (s *workflowsSuite) SetupTest() {
	s.env = s.WorkflowTestSuite.NewTestWorkflowEnvironment()
	s.env.RegisterWorkflow(NewTestWorkflow)
	s.env.RegisterWorkflow(NewTestFixerWorkflow)
	s.env.RegisterWorkflow(GetCorruptedKeys)
}

func (s *workflowsSuite) TestScannerWorkflow_Failure_ScanShard() {
	s.env.OnActivity(ActivityScannerConfig, mock.Anything, mock.Anything).Return(ResolvedScannerWorkflowConfig{
		GenericScannerConfig: GenericScannerConfig{
			Enabled:           true,
			Concurrency:       3,
			ActivityBatchSize: 5,
		},
	}, nil)
	shards := Shards{
		Range: &ShardRange{
			Min: 0,
			Max: 30,
		},
	}

	batches := [][]int{
		{0, 3, 6, 9, 12},
		{15, 18, 21, 24, 27},
		{1, 4, 7, 10, 13},
		{16, 19, 22, 25, 28},
		{2, 5, 8, 11, 14},
		{17, 20, 23, 26, 29},
	}

	for i, batch := range batches {

		var reports []ScanReport
		var err error
		if i == len(batches)-1 {
			reports = nil
			err = errors.New("scan shard activity got error")
		} else {
			err = nil
			for _, s := range batch {
				reports = append(reports, ScanReport{
					ShardID: s,
					Stats: ScanStats{
						EntitiesCount: 10,
					},
					Result: ScanResult{
						ControlFlowFailure: &ControlFlowFailure{
							Info: "got control flow failure",
						},
					},
				})
			}
		}
		s.env.OnActivity(ActivityScanShard, mock.Anything, ScanShardActivityParams{
			Shards: batch,
		}).Return(reports, err)
	}
	s.env.ExecuteWorkflow(NewTestWorkflow, "test-workflow", ScannerWorkflowParams{
		Shards: shards,
	})
	s.True(s.env.IsWorkflowCompleted())
	s.Equal("scan shard activity got error", s.env.GetWorkflowError().Error())
}

func (s *workflowsSuite) TestScannerWorkflow_Failure_ScannerConfigActivity() {
	s.env.OnActivity(ActivityScannerConfig, mock.Anything, mock.Anything).Return(ResolvedScannerWorkflowConfig{}, errors.New("got error getting config"))
	s.env.ExecuteWorkflow(NewTestWorkflow, "test-workflow", ScannerWorkflowParams{
		Shards: Shards{
			List: []int{1, 2, 3},
		},
	})
	s.True(s.env.IsWorkflowCompleted())
	s.Equal("got error getting config", s.env.GetWorkflowError().Error())
}

func (s *workflowsSuite) TestScannerWorkflow_Requires_Name() {
	s.env.OnActivity(ActivityScannerConfig, mock.Anything, mock.Anything).Return(ResolvedScannerWorkflowConfig{}, errors.New("got error getting config"))
	s.env.ExecuteWorkflow(NewTestWorkflow, "", ScannerWorkflowParams{
		Shards: Shards{
			List: []int{1, 2, 3},
		},
	})
	s.True(s.env.IsWorkflowCompleted())
	s.Equal("workflow name is not provided", s.env.GetWorkflowError().Error())
}

func (s *workflowsSuite) TestScannerWorkflow_Requires_Valid_ShardConfig() {
	s.env.OnActivity(ActivityScannerConfig, mock.Anything, mock.Anything).Return(ResolvedScannerWorkflowConfig{}, errors.New("got error getting config"))
	s.env.ExecuteWorkflow(NewTestWorkflow, "test-workflow", ScannerWorkflowParams{})
	s.True(s.env.IsWorkflowCompleted())
	s.Equal("must provide either List or Range", s.env.GetWorkflowError().Error())
}

func (s *workflowsSuite) TestScannerWorkflow_Success_Disabled() {
	s.env.OnActivity(ActivityScannerConfig, mock.Anything, mock.Anything).Return(ResolvedScannerWorkflowConfig{
		GenericScannerConfig: GenericScannerConfig{
			Enabled: false,
		},
	}, nil)

	s.env.ExecuteWorkflow(NewTestWorkflow, "test-workflow", ScannerWorkflowParams{
		Shards: Shards{
			List: []int{1, 2, 3},
		},
	})

	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

func (s *workflowsSuite) TestFixerWorkflow_Success() {
	corruptedKeys := make([]CorruptedKeysEntry, 30)
	for i := 0; i < 30; i++ {
		corruptedKeys[i] = CorruptedKeysEntry{
			ShardID: i,
		}
	}
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, mock.Anything).Return(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: corruptedKeys,
		MinShard:      common.IntPtr(0),
		MaxShard:      common.IntPtr(29),
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			IsDone:      true,
			NextShardID: nil,
		},
	}, nil)

	enabledFixInvariants := CustomScannerConfig{
		// historically enabled by default
		invariant.CollectionHistory.String():      "true",
		invariant.CollectionMutableState.String(): "true",
		// disabled by default
		invariant.CollectionStale.String(): "false",
	}
	s.env.OnActivity(ActivityFixerConfig, mock.Anything, FixShardConfigParams{ /* no contents currently */ }).Return(&FixShardConfigResults{
		EnabledInvariants: enabledFixInvariants,
	}, nil)

	fixerWorkflowConfigOverwrites := FixerWorkflowConfigOverwrites{
		Concurrency:             common.IntPtr(3),
		BlobstoreFlushThreshold: common.IntPtr(1000),
		ActivityBatchSize:       common.IntPtr(5),
	}
	resolvedFixerWorkflowConfig := ResolvedFixerWorkflowConfig{
		Concurrency:             3,
		ActivityBatchSize:       5,
		BlobstoreFlushThreshold: 1000,
	}
	batches := [][]int{
		{0, 3, 6, 9, 12},
		{15, 18, 21, 24, 27},
		{1, 4, 7, 10, 13},
		{16, 19, 22, 25, 28},
		{2, 5, 8, 11, 14},
		{17, 20, 23, 26, 29},
	}

	for _, batch := range batches {
		var corruptedKeys []CorruptedKeysEntry
		for _, shard := range batch {
			corruptedKeys = append(corruptedKeys, CorruptedKeysEntry{
				ShardID: shard,
			})
		}
		var reports []FixReport
		for i, s := range batch {
			if i == 0 {
				reports = append(reports, FixReport{
					ShardID: s,
					Stats: FixStats{
						EntitiesCount: 10,
					},
					Result: FixResult{
						ControlFlowFailure: &ControlFlowFailure{
							Info: "got control flow failure",
						},
					},
				})
			} else {
				reports = append(reports, FixReport{
					ShardID: s,
					Stats: FixStats{
						EntitiesCount: 10,
						FixedCount:    2,
						SkippedCount:  1,
						FailedCount:   1,
					},
					Result: FixResult{
						ShardFixKeys: &FixKeys{
							Skipped: &store.Keys{
								UUID: "skipped_keys",
							},
							Failed: &store.Keys{
								UUID: "failed_keys",
							},
							Fixed: &store.Keys{
								UUID: "fixed_keys",
							},
						},
					},
				})
			}
		}
		s.env.OnActivity(ActivityFixShard, mock.Anything, FixShardActivityParams{
			CorruptedKeysEntries:        corruptedKeys,
			ResolvedFixerWorkflowConfig: resolvedFixerWorkflowConfig,
			EnabledInvariants:           enabledFixInvariants,
		}).Return(reports, nil)
	}

	s.env.ExecuteWorkflow(NewTestFixerWorkflow, FixerWorkflowParams{
		ScannerWorkflowWorkflowID:     "test_wid",
		ScannerWorkflowRunID:          "test_rid",
		FixerWorkflowConfigOverwrites: fixerWorkflowConfigOverwrites,
	})
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())

	aggValue, err := s.env.QueryWorkflow(AggregateReportQuery)
	s.NoError(err)
	var agg AggregateFixReportResult
	s.NoError(aggValue.Get(&agg))
	s.Equal(AggregateFixReportResult{
		EntitiesCount: 240,
		FixedCount:    48,
		FailedCount:   24,
		SkippedCount:  24,
	}, agg)

	for i := 0; i < 30; i++ {
		shardReportValue, err := s.env.QueryWorkflow(ShardReportQuery, i)
		s.NoError(err)
		var shardReport *FixReport
		s.NoError(shardReportValue.Get(&shardReport))
		if i == 0 || i == 1 || i == 2 || i == 15 || i == 16 || i == 17 {
			s.Equal(&FixReport{
				ShardID: i,
				Stats: FixStats{
					EntitiesCount: 10,
				},
				Result: FixResult{
					ControlFlowFailure: &ControlFlowFailure{
						Info: "got control flow failure",
					},
				},
			}, shardReport)
		} else {
			s.Equal(&FixReport{
				ShardID: i,
				Stats: FixStats{
					EntitiesCount: 10,
					FixedCount:    2,
					FailedCount:   1,
					SkippedCount:  1,
				},
				Result: FixResult{
					ShardFixKeys: &FixKeys{
						Skipped: &store.Keys{
							UUID: "skipped_keys",
						},
						Failed: &store.Keys{
							UUID: "failed_keys",
						},
						Fixed: &store.Keys{
							UUID: "fixed_keys",
						},
					},
				},
			}, shardReport)
		}
	}

	statusValue, err := s.env.QueryWorkflow(ShardStatusQuery, PaginatedShardQueryRequest{})
	s.NoError(err)
	var status *ShardStatusQueryResult
	s.NoError(statusValue.Get(&status))
	expected := make(map[int]ShardStatus)
	for i := 0; i < 30; i++ {
		if i == 0 || i == 1 || i == 2 || i == 15 || i == 16 || i == 17 {
			expected[i] = ShardStatusControlFlowFailure
		} else {
			expected[i] = ShardStatusSuccess
		}
	}
	s.Equal(ShardStatusResult(expected), status.Result)

	// check for paginated query result
	statusValue, err = s.env.QueryWorkflow(ShardStatusQuery, PaginatedShardQueryRequest{
		StartingShardID: common.IntPtr(5),
		LimitShards:     common.IntPtr(10),
	})
	s.NoError(err)
	status = &ShardStatusQueryResult{}
	s.NoError(statusValue.Get(&status))
	expected = make(map[int]ShardStatus)
	for i := 5; i < 15; i++ {
		if i == 0 || i == 1 || i == 2 || i == 15 || i == 16 || i == 17 {
			expected[i] = ShardStatusControlFlowFailure
		} else {
			expected[i] = ShardStatusSuccess
		}
	}
	s.Equal(ShardStatusResult(expected), status.Result)
	s.False(status.ShardQueryPaginationToken.IsDone)
	s.Equal(15, *status.ShardQueryPaginationToken.NextShardID)
}

func (s *workflowsSuite) TestGetCorruptedKeys_Success() {
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, FixerCorruptedKeysActivityParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
		StartingShardID:           nil,
	}).Return(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: []CorruptedKeysEntry{{ShardID: 1}, {ShardID: 5}, {ShardID: 10}},
		MinShard:      common.IntPtr(1),
		MaxShard:      common.IntPtr(10),
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			NextShardID: common.IntPtr(11),
			IsDone:      false,
		},
	}, nil)
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, FixerCorruptedKeysActivityParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
		StartingShardID:           common.IntPtr(11),
	}).Return(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: []CorruptedKeysEntry{{ShardID: 11}, {ShardID: 12}},
		MinShard:      common.IntPtr(11),
		MaxShard:      common.IntPtr(12),
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			NextShardID: common.IntPtr(13),
			IsDone:      false,
		},
	}, nil)
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, FixerCorruptedKeysActivityParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
		StartingShardID:           common.IntPtr(13),
	}).Return(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: []CorruptedKeysEntry{{ShardID: 20}, {ShardID: 41}},
		MinShard:      common.IntPtr(20),
		MaxShard:      common.IntPtr(41),
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			NextShardID: common.IntPtr(42),
			IsDone:      false,
		},
	}, nil)
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, FixerCorruptedKeysActivityParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
		StartingShardID:           common.IntPtr(42),
	}).Return(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: []CorruptedKeysEntry{},
		MinShard:      nil,
		MaxShard:      nil,
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			NextShardID: nil,
			IsDone:      true,
		},
	}, nil)

	s.env.ExecuteWorkflow(GetCorruptedKeys, FixerWorkflowParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
	})
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var result *FixerCorruptedKeysActivityResult
	s.NoError(s.env.GetWorkflowResult(&result))
	s.Equal(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: []CorruptedKeysEntry{
			{ShardID: 1},
			{ShardID: 5},
			{ShardID: 10},
			{ShardID: 11},
			{ShardID: 12},
			{ShardID: 20},
			{ShardID: 41},
		},
		MinShard: common.IntPtr(1),
		MaxShard: common.IntPtr(41),
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			NextShardID: nil,
			IsDone:      true,
		},
	}, result)
}

func (s *workflowsSuite) TestGetCorruptedKeys_Error() {
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, FixerCorruptedKeysActivityParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
		StartingShardID:           nil,
	}).Return(&FixerCorruptedKeysActivityResult{
		CorruptedKeys: []CorruptedKeysEntry{{ShardID: 1}, {ShardID: 5}, {ShardID: 10}},
		MinShard:      common.IntPtr(1),
		MaxShard:      common.IntPtr(10),
		ShardQueryPaginationToken: ShardQueryPaginationToken{
			NextShardID: common.IntPtr(11),
			IsDone:      false,
		},
	}, nil)
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, FixerCorruptedKeysActivityParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
		StartingShardID:           common.IntPtr(11),
	}).Return(nil, errors.New("got error"))
	s.env.ExecuteWorkflow(GetCorruptedKeys, FixerWorkflowParams{
		ScannerWorkflowWorkflowID: "test_wid",
		ScannerWorkflowRunID:      "test_rid",
	})
	s.True(s.env.IsWorkflowCompleted())
	s.Error(s.env.GetWorkflowError())
}

func (s *workflowsSuite) TestScannerWorkflow_Failure_CorruptedKeysActivity() {
	s.env.OnActivity(ActivityFixerCorruptedKeys, mock.Anything, mock.Anything).Return(nil, errors.New("got error getting corrupted keys"))
	s.env.ExecuteWorkflow(NewTestFixerWorkflow, FixerWorkflowParams{})
	s.True(s.env.IsWorkflowCompleted())
	s.Equal("got error getting corrupted keys", s.env.GetWorkflowError().Error())
}

func NewTestWorkflow(ctx workflow.Context, name string, params ScannerWorkflowParams) error {
	wf, err := NewScannerWorkflow(ctx, name, params)
	if err != nil {
		return err
	}

	return wf.Start(ctx)
}

func NewTestFixerWorkflow(ctx workflow.Context, params FixerWorkflowParams) error {
	wf, err := NewFixerWorkflow(ctx, "test-fixer", params)
	if err != nil {
		return err
	}

	return wf.Start(ctx)

}
