import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type NavItem = 'home' | 'site' | 'channel' | 'group' | 'model' | 'log' | 'setting'

const NAV_ORDER: NavItem[] = ['home', 'site', 'channel', 'group', 'model', 'log', 'setting']

interface NavState {
    activeItem: NavItem
    prevItem: NavItem | null
    direction: number
    expanded: boolean
    setActiveItem: (item: NavItem) => void
    toggleExpanded: () => void
}

export const useNavStore = create<NavState>()(
    persist(
        (set, get) => ({
            activeItem: 'home',
            prevItem: null,
            direction: 0,
            expanded: true,
            setActiveItem: (item) => {
                const { activeItem } = get()
                const currentIndex = NAV_ORDER.indexOf(activeItem)
                const newIndex = NAV_ORDER.indexOf(item)
                const direction = newIndex > currentIndex ? 1 : -1

                set({
                    activeItem: item,
                    prevItem: activeItem,
                    direction
                })
            },
            toggleExpanded: () => set((state) => ({ expanded: !state.expanded })),
        }),
        {
            name: 'nav-storage',
        }
    )
)
