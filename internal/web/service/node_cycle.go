package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// ValidateNodeTopology prevents a panel from importing its own inbounds back
// through a newly linked node. The remote status supplies its panelGuid and its
// descendants endpoint supplies the panels it already manages directly.
func (s *NodeService) ValidateNodeTopology(ctx context.Context, node *model.Node, remoteGuid string) error {
	selfGuid, err := (&SettingService{}).GetPanelGuid()
	if err != nil {
		return fmt.Errorf("read local panel identity: %w", err)
	}
	selfGuid = strings.TrimSpace(selfGuid)
	remoteGuid = strings.TrimSpace(remoteGuid)
	if selfGuid == "" || remoteGuid == "" {
		return fmt.Errorf("cannot verify node topology without both panel GUIDs; update the remote panel before linking it")
	}
	if remoteGuid == selfGuid {
		return fmt.Errorf("node link would connect this panel to itself (panel GUID %s)", selfGuid)
	}
	if node == nil {
		return fmt.Errorf("node is required for topology validation")
	}

	var descendants []model.NodeSummary
	if node.OutboundTag == "" {
		descendants, err = runtime.NewRemote(node, nil).GetDescendants(ctx)
	} else {
		s.withOutboundBridge(node.Id, node.OutboundTag, func(proxyURL string) {
			descendants, err = runtime.NewRemote(node, staticEgressResolver(proxyURL)).GetDescendants(ctx)
		})
	}
	if err != nil {
		return fmt.Errorf("cannot verify node topology; update the remote panel to support descendants: %w", err)
	}
	for _, descendant := range descendants {
		if strings.TrimSpace(descendant.Guid) == selfGuid {
			return fmt.Errorf("node link would create a cycle: the remote panel already manages this panel (panel GUID %s)", selfGuid)
		}
	}
	return nil
}
