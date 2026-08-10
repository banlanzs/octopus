'use client';

import { memo, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { Pencil, Trash2, Upload } from 'lucide-react';
import { motion, AnimatePresence } from 'motion/react';
import { useTranslations } from 'next-intl';
import {
    useUpdateChannelModelPrice,
    useDeleteChannelModelPrice,
    type LLMChannel,
} from '@/api/endpoints/model';
import { useUpdateChannel } from '@/api/endpoints/channel';
import { getModelIcon } from '@/lib/model-icons';
import { toast } from '@/components/common/Toast';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { ChannelModelDeleteOverlay, ChannelModelEditOverlay } from './ItemOverlays';
import { cn } from '@/lib/utils';
import { createPortal } from 'react-dom';

type ChannelGroup = {
    channel_id: number;
    channel_name: string;
    models: LLMChannel[];
};

interface ModelItemProps {
    group: ChannelGroup;
    layout?: 'grid' | 'list';
}

type EditValues = {
    input: string;
    output: string;
    cache_read: string;
    cache_write: string;
};

function priceOf(model: LLMChannel): EditValues {
    const p = model.price;
    return {
        input: (p?.input ?? 0).toString(),
        output: (p?.output ?? 0).toString(),
        cache_read: (p?.cache_read ?? 0).toString(),
        cache_write: (p?.cache_write ?? 0).toString(),
    };
}

function ChannelMultiplierEditor({ channelId, multiplier }: { channelId: number; multiplier: number }) {
    const t = useTranslations('model');
    const updateChannel = useUpdateChannel();
    const [editing, setEditing] = useState(false);
    const [value, setValue] = useState(multiplier.toString());
    const inputRef = useRef<HTMLInputElement | null>(null);

    // 当外部 multiplier 变化时同步本地显示值
    useEffect(() => {
        if (!editing) setValue(multiplier.toString());
    }, [multiplier, editing]);

    useEffect(() => {
        if (editing) inputRef.current?.select();
    }, [editing]);

    const submit = () => {
        const next = parseFloat(value);
        if (!isNaN(next) && next > 0 && next !== multiplier) {
            updateChannel.mutate(
                { id: channelId, price_multiplier: next },
                {
                    onSuccess: () => {
                        toast.success(t('toast.multiplierUpdated'));
                    },
                    onError: (error) => {
                        toast.error(t('toast.updateFailed'), { description: error.message });
                        setValue(multiplier.toString());
                    },
                }
            );
        } else {
            setValue(multiplier.toString());
        }
        setEditing(false);
    };

    if (editing) {
        return (
            <input
                ref={inputRef}
                type="number"
                step="any"
                min="0"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                onBlur={submit}
                onKeyDown={(e) => {
                    if (e.key === 'Enter') submit();
                    if (e.key === 'Escape') {
                        setValue(multiplier.toString());
                        setEditing(false);
                    }
                }}
                className="w-16 h-6 px-1.5 text-xs tabular-nums rounded-md border border-border bg-background text-foreground outline-none"
                title={t('multiplierHint')}
            />
        );
    }

    return (
        <Tooltip side="bottom" sideOffset={6}>
            <TooltipTrigger asChild>
                <button
                    type="button"
                    onClick={() => {
                        setValue(multiplier.toString());
                        setEditing(true);
                    }}
                    className="h-6 px-2 text-xs tabular-nums rounded-md border border-border/60 bg-muted/30 text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-colors inline-flex items-center gap-1"
                >
                    <span>×</span>
                    <span>{multiplier}</span>
                </button>
            </TooltipTrigger>
            <TooltipContent>
                {t('multiplier')}
                <br />
                {t('multiplierHint')}
            </TooltipContent>
        </Tooltip>
    );
}

export const ChannelModelItem = memo(function ChannelModelItem({
    model,
}: {
    model: LLMChannel;
}) {
    const t = useTranslations('model');
    const instanceId = useId();
    const [isEditOpen, setIsEditOpen] = useState(false);
    const [confirmDelete, setConfirmDelete] = useState(false);
    const [overlayRect, setOverlayRect] = useState<{ top: number; left: number; width: number } | null>(null);
    const cardRef = useRef<HTMLElement | null>(null);
    const editButtonRef = useRef<HTMLButtonElement | null>(null);
    const editOverlayRef = useRef<HTMLDivElement | null>(null);
    const [editValues, setEditValues] = useState<EditValues>(() => priceOf(model));

    const editLayoutId = `edit-btn-${model.channel_id}-${model.name}-${instanceId}`;
    const deleteLayoutId = `delete-btn-${model.channel_id}-${model.name}-${instanceId}`;

    const updatePrice = useUpdateChannelModelPrice();
    const deletePrice = useDeleteChannelModelPrice();
    const hasChannelPrice = !!model.has_channel_price;

    const { Avatar: ModelAvatar, color: brandColor } = useMemo(() => getModelIcon(model.name), [model.name]);

    const updateOverlayRect = useCallback(() => {
        const card = cardRef.current;
        if (!card) return;
        const rect = card.getBoundingClientRect();
        setOverlayRect((prev) => {
            if (prev && prev.top === rect.top && prev.left === rect.left && prev.width === rect.width) {
                return prev;
            }
            return { top: rect.top, left: rect.left, width: rect.width };
        });
    }, []);

    const closeEdit = useCallback(() => setIsEditOpen(false), []);

    const handleEditClick = () => {
        setConfirmDelete(false);
        setEditValues(priceOf(model));
        updateOverlayRect();
        setIsEditOpen(true);
    };

    const handleSaveEdit = () => {
        const { input, output, cache_read, cache_write } = editValues;
        // 无法区分"未配置渠道价" vs "显式配置差价"——为避免覆盖用户的全局价，
        // 这里仅当用户显式保存时写入渠道价。
        updatePrice.mutate(
            {
                channel_id: model.channel_id,
                model_name: model.name,
                input: parseFloat(input) || 0,
                output: parseFloat(output) || 0,
                cache_read: parseFloat(cache_read) || 0,
                cache_write: parseFloat(cache_write) || 0,
            },
            {
                onSuccess: () => {
                    closeEdit();
                    toast.success(t('toast.channelUpdated'));
                },
                onError: (error) => {
                    toast.error(t('toast.updateFailed'), { description: error.message });
                },
            }
        );
    };

    const handleDeleteClick = () => {
        closeEdit();
        setConfirmDelete(true);
    };
    const handleCancelDelete = () => setConfirmDelete(false);
    const handleConfirmDelete = () => {
        deletePrice.mutate(
            { channel_id: model.channel_id, model_name: model.name },
            {
                onSuccess: () => {
                    setConfirmDelete(false);
                    toast.success(t('toast.restored'));
                },
                onError: (error) => {
                    setConfirmDelete(false);
                    toast.error(t('toast.restoreFailed'), { description: error.message });
                },
            }
        );
    };

    useEffect(() => {
        if (!isEditOpen) return;
        const handlePointerDown = (event: PointerEvent) => {
            const target = event.target as Node | null;
            if (!target) return;
            if (editOverlayRef.current?.contains(target)) return;
            if (editButtonRef.current?.contains(target)) return;
            closeEdit();
        };
        const handleKeyDown = (event: KeyboardEvent) => {
            if (event.key === 'Escape') closeEdit();
        };
        updateOverlayRect();
        window.addEventListener('resize', updateOverlayRect);
        window.addEventListener('scroll', updateOverlayRect, true);
        document.addEventListener('pointerdown', handlePointerDown);
        document.addEventListener('keydown', handleKeyDown);
        return () => {
            window.removeEventListener('resize', updateOverlayRect);
            window.removeEventListener('scroll', updateOverlayRect, true);
            document.removeEventListener('pointerdown', handlePointerDown);
            document.removeEventListener('keydown', handleKeyDown);
        };
    }, [isEditOpen, updateOverlayRect, closeEdit]);

    const shouldRenderEditPortal = isEditOpen || overlayRect !== null;

    return (
        <article
            ref={cardRef}
            className={cn(
                'group relative rounded-3xl border border-border bg-card transition-all duration-300 flex items-center gap-3 p-3',
                (isEditOpen || confirmDelete) && 'z-50'
            )}
        >
            <ModelAvatar size={40} />
            <div className="flex-1 min-w-0 flex flex-col justify-center gap-1">
                <Tooltip side="top" sideOffset={10} align="start">
                    <TooltipTrigger className="text-sm font-semibold text-card-foreground leading-tight truncate">
                        {model.name}
                    </TooltipTrigger>
                    <TooltipContent key={model.name}>
                        {model.name}
                    </TooltipContent>
                </Tooltip>
                <p className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span className="tabular-nums">
                        {t('overlay.input')}: {editValues.input}
                    </span>
                    <span>/</span>
                    <span className="tabular-nums">
                        {t('overlay.output')}: {editValues.output}
                    </span>
                    {!hasChannelPrice && (
                        <span className="text-muted-foreground/50">(global)</span>
                    )}
                </p>
            </div>

            <div className="shrink-0 flex items-center gap-1.5">
                <motion.button
                    ref={editButtonRef}
                    layoutId={editLayoutId}
                    type="button"
                    onClick={handleEditClick}
                    disabled={isEditOpen || confirmDelete}
                    className="h-8 w-8 flex items-center justify-center rounded-lg bg-muted/60 text-muted-foreground transition-colors hover:bg-muted disabled:opacity-50"
                    title={t('card.edit')}
                >
                    <Pencil className="size-3.5" />
                </motion.button>
                <motion.button
                    layoutId={deleteLayoutId}
                    type="button"
                    onClick={handleDeleteClick}
                    disabled={isEditOpen || confirmDelete}
                    className={cn(
                        'h-8 w-8 flex items-center justify-center rounded-lg transition-colors disabled:opacity-50',
                        hasChannelPrice
                            ? 'bg-muted/60 text-muted-foreground hover:bg-muted'
                            : 'bg-muted/30 text-muted-foreground/50 cursor-not-allowed'
                    )}
                    title={t('restoreDefault')}
                >
                    <Upload className="size-3.5" />
                </motion.button>
            </div>

            <AnimatePresence>
                {confirmDelete && (
                    <ChannelModelDeleteOverlay
                        layoutId={deleteLayoutId}
                        isPending={deletePrice.isPending}
                        onCancel={handleCancelDelete}
                        onConfirm={handleConfirmDelete}
                    />
                )}
            </AnimatePresence>

            {shouldRenderEditPortal && typeof document !== 'undefined'
                ? createPortal(
                      <AnimatePresence onExitComplete={() => setOverlayRect(null)}>
                          {isEditOpen && overlayRect && (
                              <div
                                  ref={editOverlayRef}
                                  className="fixed z-[90]"
                                  style={{
                                      top: `${overlayRect.top}px`,
                                      left: `${overlayRect.left}px`,
                                      width: `${overlayRect.width}px`,
                                  }}
                              >
                                  <div className="relative">
                                      <ChannelModelEditOverlay
                                          layoutId={editLayoutId}
                                          modelName={model.name}
                                          brandColor={brandColor}
                                          editValues={editValues}
                                          isPending={updatePrice.isPending}
                                          onChange={setEditValues}
                                          onCancel={closeEdit}
                                          onSave={handleSaveEdit}
                                      />
                                  </div>
                              </div>
                          )}
                      </AnimatePresence>,
                      document.body
                  )
                : null}
        </article>
    );
});

export const ModelItem = memo(function ModelItem({
    group,
    layout = 'grid',
}: ModelItemProps) {
    const t = useTranslations('model');

    return (
        <div className="rounded-3xl border border-border bg-card/40 p-4 flex flex-col gap-2">
            <header className="flex items-center justify-between gap-2">
                <h3 className="text-sm font-semibold text-card-foreground truncate flex items-center gap-2">
                    <span className="inline-block h-2 w-2 rounded-full bg-primary/60" />
                    {t('channelGroup', { name: group.channel_name })}
                </h3>
                <div className="flex items-center gap-2 shrink-0">
                    <ChannelMultiplierEditor
                        channelId={group.channel_id}
                        multiplier={group.models[0]?.price_multiplier ?? 1}
                    />
                    <span className="text-xs text-muted-foreground">
                        {t('models')}: {group.models.length}
                    </span>
                </div>
            </header>
            <div className="flex flex-col gap-2">
                {group.models.map((model) => (
                    <ChannelModelItem key={`${model.channel_id}-${model.name}`} model={model} />
                ))}
            </div>
        </div>
    );
});