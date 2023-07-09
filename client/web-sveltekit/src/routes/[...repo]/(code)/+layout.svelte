<script lang="ts">
    import { mdiChevronDoubleLeft, mdiChevronDoubleRight } from '@mdi/js'

    import { page } from '$app/stores'
    import { isErrorLike } from '$lib/common'
    import Icon from '$lib/Icon.svelte'
    import { FileTreeProvider } from '$lib/repo/api/tree'
    import Separator, { getSeparatorPosition } from '$lib/Separator.svelte'

    import type { PageData } from './$types'
    import FileTree from './FileTree.svelte'

    export let data: PageData

    let treeProvider: FileTreeProvider | null = null

    async function updateFileTreeProvider(repoName: string, revision: string | undefined, parentPath: string) {
        treeProvider = null
        const commit = await data.commitWithTree.deferred

        // Do nothing if update was called with new arguments in the meantime
        if (repoName !== data.repoName || revision !== data.revision || parentPath !== data.parentPath) {
            return
        }

        treeProvider =
            !isErrorLike(commit) && commit?.tree
                ? new FileTreeProvider({
                      tree: commit.tree,
                      repoName,
                      revision: revision ?? '',
                      commitID: commit.oid,
                  })
                : null
    }

    // Only update the tree provider (which causes the file tree to rerender) if the new file tree would be rooted at an
    // ancestor of the current file tree
    $: ({ repoName, revision, parentPath } = data)
    $: updateFileTreeProvider(repoName, revision, parentPath)

    let showSidebar = true
    const sidebarSize = getSeparatorPosition('repo-sidebar', 0.2)
    $: sidebarWidth = showSidebar ? `max(200px, ${$sidebarSize * 100}%)` : undefined
</script>

<section>
    <div class="sidebar" class:open={showSidebar} style:min-width={sidebarWidth} style:max-width={sidebarWidth}>
        {#if showSidebar && treeProvider}
            <h3>
                Files
                <button on:click={() => (showSidebar = false)}><Icon svgPath={mdiChevronDoubleLeft} inline /></button>
            </h3>
            <FileTree {treeProvider} selectedPath={$page.params.path ?? ''} />
        {/if}
        {#if !showSidebar}
            <button class="open-sidebar" on:click={() => (showSidebar = true)}
                ><Icon svgPath={mdiChevronDoubleRight} inline /></button
            >
        {/if}
    </div>
    {#if showSidebar}
        <Separator currentPosition={sidebarSize} />
    {/if}
    <div class="content">
        <slot />
    </div>
</section>

<style lang="scss">
    section {
        display: flex;
        overflow: hidden;
        margin: 1rem;
        margin-bottom: 0;
        flex: 1;
    }

    .sidebar {
        &.open {
            width: 200px;
        }

        overflow: hidden;
        display: flex;
        flex-direction: column;
    }

    .content {
        flex: 1;
        margin: 0 1rem;
        background-color: var(--code-bg);
        overflow: hidden;
        display: flex;
        flex-direction: column;
        border: 1px solid var(--border-color);
        border-radius: var(--border-radius);
    }

    button {
        border: 0;
        background-color: transparent;
        padding: 0;
        margin: 0;
        cursor: pointer;
    }

    h3 {
        display: flex;
        justify-content: space-between;
        align-items: center;
    }

    .open-sidebar {
        position: absolute;
        left: 0;
        border: 1px solid var(--border-color);
    }
</style>
