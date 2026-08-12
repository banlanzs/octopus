"use client"

import { motion, AnimatePresence } from "motion/react"
import { cn } from "@/lib/utils"
import { useNavStore, type NavItem } from "@/components/modules/navbar"
import { ROUTES } from "@/route/config"
import { usePreload } from "@/route/use-preload"
import { ENTRANCE_VARIANTS } from "@/lib/animations/fluid-transitions"
import { useTranslations } from 'next-intl'
import { Menu } from "lucide-react"

export function NavBar() {
    const { activeItem, setActiveItem, expanded, toggleExpanded } = useNavStore()
    const { preload } = usePreload()
    const t = useTranslations('navbar')

    return (
        <div className="relative z-50 md:min-h-screen">
            <motion.nav
                aria-label="Main Navigation"
                className={cn(
                    "fixed bottom-6 left-1/2 -translate-x-1/2 flex items-center gap-1 p-3",
                    "md:sticky md:top-30 md:left-auto md:bottom-auto md:translate-x-0 md:flex-col md:gap-2 md:p-3",
                    "bg-sidebar text-sidebar-foreground border border-sidebar-border rounded-3xl",
                    "custom-shadow"
                )}
                variants={ENTRANCE_VARIANTS.navbar}
                initial="initial"
                animate="animate"
            >
                {/* 展开/折叠切换按钮 — 仅桌面端显示 */}
                <motion.button
                    type="button"
                    aria-label={expanded ? t('collapse') : t('expand')}
                    title={expanded ? t('collapse') : t('expand')}
                    onClick={toggleExpanded}
                    className={cn(
                        "relative hidden md:flex p-2 md:p-3 rounded-2xl z-20",
                        "text-sidebar-foreground/60 hover:bg-sidebar-accent hover:text-sidebar-foreground"
                    )}
                    whileHover={{ scale: 1.1, zIndex: 30 }}
                    whileTap={{ scale: 0.95 }}
                >
                    <motion.span
                        animate={{ rotate: expanded ? 90 : 0 }}
                        transition={{ duration: 0.25 }}
                    >
                        <Menu strokeWidth={2} />
                    </motion.span>
                </motion.button>

                {ROUTES.map((route, index) => {
                    const isActive = activeItem === route.id
                    const label = t(route.id)
                    return (
                        <motion.button
                            key={route.id}
                            type="button"
                            onClick={() => setActiveItem(route.id as NavItem)}
                            onMouseEnter={() => preload(route.id)}
                            aria-label={label}
                            title={!expanded ? label : undefined}
                            className={cn(
                                "relative flex items-center gap-2 z-20",
                                isActive
                                    ? "text-sidebar-primary-foreground"
                                    : "text-sidebar-foreground/60 hover:bg-sidebar-accent hover:text-sidebar-foreground",
                                expanded
                                    ? "w-full p-2 md:px-4 md:py-3 rounded-2xl"
                                    : "p-2 md:p-3 rounded-2xl"
                            )}
                            initial={{ opacity: 0, scale: 0.8 }}
                            animate={{
                                opacity: 1,
                                scale: 1,
                                transition: {
                                    delay: index * 0.05,
                                    duration: 0.3,
                                }
                            }}
                            whileHover={{ scale: 1.1, zIndex: 30 }}
                            whileTap={{ scale: 0.95 }}
                        >
                            {isActive && (
                                <motion.div
                                    layoutId="navbar-indicator"
                                    className="absolute inset-0 bg-sidebar-primary rounded-2xl z-0"
                                    transition={{ type: "spring", stiffness: 300, damping: 30 }}
                                />
                            )}
                            <span className="relative z-10 flex items-center gap-2">
                                <route.icon strokeWidth={2} />
                                <AnimatePresence initial={false}>
                                    {expanded && (
                                        <motion.span
                                            key="label"
                                            className="hidden md:inline text-sm font-medium whitespace-nowrap overflow-hidden"
                                            initial={{ opacity: 0, width: 0 }}
                                            animate={{ opacity: 1, width: "auto" }}
                                            exit={{ opacity: 0, width: 0 }}
                                            transition={{ duration: 0.15 }}
                                        >
                                            {label}
                                        </motion.span>
                                    )}
                                </AnimatePresence>
                            </span>
                        </motion.button>
                    )
                })}
            </motion.nav>
        </div>
    )
}
