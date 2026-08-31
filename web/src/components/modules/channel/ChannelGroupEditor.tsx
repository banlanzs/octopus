'use client';

import { useMemo, useState } from 'react';
import { Check, GripVertical, Layers, Plus, Search, Trash2, X } from 'lucide-react';
import {
    DragDropContext,
    Draggable,
    Droppable,
    type DraggableProvided,
    type DropResult,
} from '@hello-pangea/dnd';
import { useTranslations } from 'next-intl';
import { cn } from '@/lib/utils';
import { getModelIcon } from '@/lib/model-icons';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import type { ChannelGroup, ChannelGroupItem } from '@/api/endpoints/channel';
import { GroupMode } from '@/api/endpoints/group';

function reorderList<T>(list: T[], startIndex: number, endIndex: number): T[] {
    const result = [...list];
    const [removed] = result.splice(startIndex, 1);
    result.splice(endIndex, 0, removed);
    return result;
}

// ModelPicker 候选模型面板：搜索过滤 + 点击加入已选，已选标记 ✓。
// 交互对齐 group/Editor 的 ModelPickerSection，但候选项是纯模型名字符串。
function ModelPicker({
    candidates,
    selectedModels,
    onAdd,
}: {
    candidates: string[];
    selectedModels: Set<string>;
    onAdd: (model: string) => void;
}) {
    const t = useTranslations('channel.form');
    const [search, setSearch] = useState('');
    const normalized = search.trim().toLowerCase();

    const filtered = useMemo(() => {
        const sorted = [...candidates].sort((a, b) => a.localeCompare(b));
        if (!normalized) return sorted;
        return sorted.filter((c) => c.toLowerCase().includes(normalized));
    }, [candidates, normalized]);

    return (
        <div className="rounded-lg border border-border/50 bg-muted/20">
            <div className="flex items-center justify-between gap-2 px-2.5 py-1.5 border-b border-border/30 bg-muted/40">
                <span className="text-xs font-medium text-foreground">{t('channelGroupAddModel')}</span>
                <div className="relative w-28">
                    <Search className="pointer-events-none absolute left-1.5 top-1/2 size-3 -translate-y-1/2 text-muted-foreground" />
                    <Input
                        value={search}
                        onChange={(e) => setSearch(e.target.value)}
                        placeholder={t('channelGroupSearch')}
                        className="h-6 rounded-md border-border/60 bg-background/70 pl-6 pr-1.5 text-xs shadow-none focus-visible:border-border/60 focus-visible:ring-0"
                        aria-label="search"
                    />
                </div>
            </div>
            <div className="max-h-32 min-h-8 overflow-y-auto p-1.5">
                {filtered.length > 0 ? (
                    <div className="flex flex-col gap-1">
                        {filtered.map((model) => {
                            const isSelected = selectedModels.has(model);
                            const { Avatar } = getModelIcon(model);
                            return (
                                <button
                                    key={model}
                                    type="button"
                                    onClick={() => !isSelected && onAdd(model)}
                                    disabled={isSelected}
                                    className={cn(
                                        'w-full flex items-center justify-between gap-2 rounded-md border border-border/40 bg-background px-2 py-1.5 text-left transition-colors',
                                        isSelected ? 'opacity-60 cursor-not-allowed' : 'hover:bg-muted'
                                    )}
                                >
                                    <span className="flex items-center gap-1.5 min-w-0">
                                        <Avatar size={14} />
                                        <span className="text-xs font-medium truncate">{model}</span>
                                    </span>
                                    <span className="shrink-0 text-muted-foreground">
                                        {isSelected ? (
                                            <Check className="size-3.5 text-primary" />
                                        ) : (
                                            <Plus className="size-3.5" />
                                        )}
                                    </span>
                                </button>
                            );
                        })}
                    </div>
                ) : (
                    <div className="px-2 py-1.5 text-xs text-muted-foreground">
                        {candidates.length === 0 ? t('modelPickEmpty') : t('modelPickNoMatch')}
                    </div>
                )}
            </div>
        </div>
    );
}

type ItemDnd = {
    innerRef: DraggableProvided['innerRef'];
    draggableProps: DraggableProvided['draggableProps'];
    dragHandleProps: DraggableProvided['dragHandleProps'] | undefined;
    isDragging: boolean;
};

// MemberRow 已选条目行：序号徽标 + 拖拽手柄 + 模型图标 + 内联权重 + 移除。
// 视觉对齐 group/ItemList 的 MemberItem，但操作对象是 ChannelGroupItem。
function MemberRow({
    item,
    index,
    showWeight,
    onRemove,
    onWeightChange,
    dnd,
}: {
    item: ChannelGroupItem;
    index: number;
    showWeight: boolean;
    onRemove: () => void;
    onWeightChange: (weight: number) => void;
    dnd: ItemDnd;
}) {
    const { Avatar } = getModelIcon(item.model);
    return (
        <div
            // eslint-disable-next-line react-hooks/refs
            ref={dnd.innerRef}
            // eslint-disable-next-line react-hooks/refs
            {...dnd.draggableProps}
            className="rounded-lg flex items-center gap-2 bg-background border border-border/50 px-2 py-1.5 select-none"
            // eslint-disable-next-line react-hooks/refs
            style={{
                /* eslint-disable-next-line react-hooks/refs */
                ...(dnd.draggableProps?.style ?? {}),
                /* eslint-disable-next-line react-hooks/refs */
                ...(dnd.isDragging ? { zIndex: 50, boxShadow: '0 8px 32px rgba(0,0,0,0.15)' } : null),
            }}
        >
            <span className="size-4 rounded-md text-[10px] font-bold grid place-items-center shrink-0 bg-primary/10 text-primary">
                {index + 1}
            </span>
            <div
                className="p-0.5 rounded touch-none cursor-grab active:cursor-grabbing hover:bg-muted"
                // eslint-disable-next-line react-hooks/refs
                {...dnd.dragHandleProps}
            >
                <GripVertical className="size-3.5 text-muted-foreground" />
            </div>
            <Avatar size={14} />
            <span className="text-xs font-medium truncate flex-1 min-w-0">{item.model}</span>
            {showWeight && (
                <input
                    type="number"
                    min={1}
                    value={item.weight}
                    onChange={(e) => onWeightChange(Math.max(1, parseInt(e.target.value) || 1))}
                    title="weight"
                    className="w-12 h-6 text-xs text-center rounded border border-border bg-muted/50 focus:outline-none focus:ring-1 focus:ring-primary"
                />
            )}
            <button
                type="button"
                onClick={onRemove}
                className="p-1 rounded hover:bg-destructive/10 hover:text-destructive transition-colors"
            >
                <X className="size-3" />
            </button>
        </div>
    );
}

// DndList 已选条目拖拽列表，priority 由拖拽顺序隐式决定（index+1）。
function DndList({
    items,
    showWeight,
    onReorder,
    onRemove,
    onWeightChange,
    scopeId,
}: {
    items: ChannelGroupItem[];
    showWeight: boolean;
    onReorder: (next: ChannelGroupItem[]) => void;
    onRemove: (itemIdx: number) => void;
    onWeightChange: (itemIdx: number, weight: number) => void;
    scopeId: string;
}) {
    const handleDragEnd = (result: DropResult) => {
        const { destination, source } = result;
        if (!destination || destination.index === source.index) return;
        onReorder(reorderList(items, source.index, destination.index));
    };

    return (
        <DragDropContext onDragEnd={handleDragEnd}>
            <Droppable droppableId={`items-${scopeId}`}>
                {(provided) => (
                    <div
                        ref={provided.innerRef}
                        {...provided.droppableProps}
                        className="flex flex-col gap-1"
                    >
                        {items.map((item, idx) => (
                            <Draggable
                                key={`${scopeId}-${item.model}`}
                                draggableId={`${scopeId}-${item.model}`}
                                index={idx}
                            >
                                {(prov, snap) => (
                                    <MemberRow
                                        item={item}
                                        index={idx}
                                        showWeight={showWeight}
                                        onRemove={() => onRemove(idx)}
                                        onWeightChange={(w) => onWeightChange(idx, w)}
                                        dnd={{
                                            innerRef: prov.innerRef,
                                            draggableProps: prov.draggableProps,
                                            dragHandleProps: prov.dragHandleProps,
                                            isDragging: snap.isDragging,
                                        }}
                                    />
                                )}
                            </Draggable>
                        ))}
                        {provided.placeholder}
                    </div>
                )}
            </Droppable>
        </DragDropContext>
    );
}

export interface ChannelGroupEditorProps {
    groups: ChannelGroup[];
    modelCandidates: string[];
    onChange: (groups: ChannelGroup[]) => void;
}

// ChannelGroupEditor 渠道级模型分组编辑器：每个分组为一张卡片，
// 内含「别名/模式」表头 + 候选模型面板 + 已选拖拽列表。
// 模型选择交互对齐 group/Editor（搜索点选 + 已选打勾），priority 由拖拽顺序决定。
export function ChannelGroupEditor({ groups, modelCandidates, onChange }: ChannelGroupEditorProps) {
    const t = useTranslations('channel.form');

    const update = (idx: number, patch: Partial<ChannelGroup>) =>
        onChange(groups.map((g, i) => (i === idx ? { ...g, ...patch } : g)));

    const removeGroup = (idx: number) => onChange(groups.filter((_, i) => i !== idx));

    const addItem = (groupIdx: number, model: string) =>
        onChange(
            groups.map((g, i) =>
                i === groupIdx && !g.items.some((it) => it.model === model)
                    ? { ...g, items: [...g.items, { model, priority: g.items.length + 1, weight: 1 }] }
                    : g
            )
        );

    const removeItem = (groupIdx: number, itemIdx: number) =>
        onChange(
            groups.map((g, i) =>
                i === groupIdx
                    ? {
                          ...g,
                          items: g.items
                              .filter((_, j) => j !== itemIdx)
                              .map((it, j) => ({ ...it, priority: j + 1 })),
                      }
                    : g
            )
        );

    const reorderItems = (groupIdx: number, next: ChannelGroupItem[]) =>
        onChange(
            groups.map((g, i) =>
                i === groupIdx
                    ? { ...g, items: next.map((it, j) => ({ ...it, priority: j + 1 })) }
                    : g
            )
        );

    const setWeight = (groupIdx: number, itemIdx: number, weight: number) =>
        onChange(
            groups.map((g, i) =>
                i === groupIdx
                    ? { ...g, items: g.items.map((it, j) => (j === itemIdx ? { ...it, weight } : it)) }
                    : g
            )
        );

    const clearItems = (groupIdx: number) =>
        onChange(groups.map((g, i) => (i === groupIdx ? { ...g, items: [] } : g)));

    return (
        <div className="space-y-2">
            {groups.map((group, groupIdx) => {
                const selectedModels = new Set(group.items.map((it) => it.model));
                const showWeight = group.mode === GroupMode.Weighted;
                return (
                    <div
                        key={`cg-${groupIdx}`}
                        className="space-y-1.5 rounded-xl border border-border/60 bg-muted/30 p-2.5"
                    >
                        {/* 表头：别名 / 模式 / 移除分组 */}
                        <div className="flex items-center gap-2">
                            <Input
                                type="text"
                                value={group.alias}
                                onChange={(e) => update(groupIdx, { alias: e.target.value })}
                                placeholder={t('channelGroupAlias')}
                                className="rounded-xl flex-1 h-8 text-sm"
                            />
                            <Select
                                value={String(group.mode)}
                                onValueChange={(v) =>
                                    update(groupIdx, { mode: Number(v) as ChannelGroup['mode'] })
                                }
                            >
                                <SelectTrigger
                                    className="rounded-xl w-28 shrink-0 h-8 text-sm"
                                    title={t('channelGroupModeWeighted')}
                                >
                                    <SelectValue />
                                </SelectTrigger>
                                <SelectContent className="rounded-xl">
                                    <SelectItem className="rounded-xl" value={String(GroupMode.Weighted)}>
                                        {t('channelGroupModeWeighted')}
                                    </SelectItem>
                                    <SelectItem className="rounded-xl" value={String(GroupMode.Failover)}>
                                        {t('channelGroupModeFailover')}
                                    </SelectItem>
                                </SelectContent>
                            </Select>
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => removeGroup(groupIdx)}
                                className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent"
                                title={t('channelGroupRemove')}
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>

                        {/* 候选模型面板 */}
                        <ModelPicker
                            candidates={modelCandidates}
                            selectedModels={selectedModels}
                            onAdd={(model) => addItem(groupIdx, model)}
                        />

                        {/* 已选条目列表 */}
                        <div className="rounded-lg border border-border/50 bg-muted/20">
                            <div className="flex items-center justify-between px-2.5 py-1.5 border-b border-border/30 bg-muted/40">
                                <span className="text-xs font-medium text-foreground">
                                    {t('channelGroupSelected')}
                                    {group.items.length > 0 && (
                                        <span className="ml-1 text-xs text-muted-foreground font-normal">
                                            ({group.items.length})
                                        </span>
                                    )}
                                </span>
                                <button
                                    type="button"
                                    onClick={() => clearItems(groupIdx)}
                                    disabled={group.items.length === 0}
                                    className={cn(
                                        'flex items-center gap-1 px-1.5 py-0.5 rounded-md text-xs font-medium transition-colors',
                                        group.items.length === 0
                                            ? 'text-muted-foreground/50 cursor-not-allowed'
                                            : 'hover:bg-muted text-muted-foreground hover:text-foreground'
                                    )}
                                    title={t('channelGroupClear')}
                                >
                                    <Trash2 className="size-3" />
                                    <span>{t('channelGroupClear')}</span>
                                </button>
                            </div>
                            <div className="p-1.5">
                                {group.items.length > 0 ? (
                                    <DndList
                                        items={group.items}
                                        showWeight={showWeight}
                                        onReorder={(next) => reorderItems(groupIdx, next)}
                                        onRemove={(itemIdx) => removeItem(groupIdx, itemIdx)}
                                        onWeightChange={(itemIdx, w) => setWeight(groupIdx, itemIdx, w)}
                                        scopeId={`cg-${groupIdx}`}
                                    />
                                ) : (
                                    <div className="flex items-center justify-center gap-1.5 h-8 text-xs text-muted-foreground">
                                        <Layers className="size-3.5 opacity-50" />
                                        {t('channelGroupEmpty')}
                                    </div>
                                )}
                            </div>
                        </div>
                    </div>
                );
            })}
        </div>
    );
}
