'use client';

import { useMemo } from 'react';
import { useModelChannelList, type LLMChannel } from '@/api/endpoints/model';
import { ModelItem } from './Item';
import { useSearchStore, useToolbarViewOptionsStore } from '@/components/modules/toolbar';
import { VirtualizedGrid } from '@/components/common/VirtualizedGrid';

type ChannelGroup = {
    channel_id: number;
    channel_name: string;
    models: LLMChannel[];
};

function groupByChannel(rows: LLMChannel[]): ChannelGroup[] {
    const map = new Map<number, ChannelGroup>();
    for (const row of rows) {
        const group = map.get(row.channel_id);
        if (group) {
            group.models.push(row);
        } else {
            map.set(row.channel_id, {
                channel_id: row.channel_id,
                channel_name: row.channel_name,
                models: [row],
            });
        }
    }
    return Array.from(map.values()).sort((a, b) => a.channel_name.localeCompare(b.channel_name));
}

export function Model() {
    const { data: channelModels } = useModelChannelList();
    const pageKey = 'model' as const;
    const searchTerm = useSearchStore((s) => s.getSearchTerm(pageKey));
    const layout = useToolbarViewOptionsStore((s) => s.getLayout(pageKey));
    const sortOrder = useToolbarViewOptionsStore((s) => s.getSortOrder(pageKey));

    const filteredGroups = useMemo(() => {
        if (!channelModels) return [];
        const term = searchTerm.toLowerCase().trim();
        const filtered = term
            ? channelModels.filter(
                  (m) =>
                      m.name.toLowerCase().includes(term) ||
                      m.channel_name.toLowerCase().includes(term)
              )
            : [...channelModels];
        const groups = groupByChannel(filtered);
        // 对每个渠道内的模型按名称排序
        for (const group of groups) {
            group.models.sort((a, b) =>
                sortOrder === 'asc' ? a.name.localeCompare(b.name) : b.name.localeCompare(a.name)
            );
        }
        return groups;
    }, [channelModels, searchTerm, sortOrder]);

    return (
        <VirtualizedGrid
            items={filteredGroups}
            layout={layout}
            columns={{ default: 1, md: 1, lg: 1 }}
            estimateItemHeight={160}
            getItemKey={(group) => `channel-group-${group.channel_id}`}
            renderItem={(group) => <ModelItem group={group} layout={layout} />}
        />
    );
}
