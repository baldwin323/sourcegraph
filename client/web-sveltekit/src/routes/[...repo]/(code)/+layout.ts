import { dirname } from 'path'

import { catchError } from 'rxjs/operators'

import { asError, isErrorLike, type ErrorLike } from '$lib/common'
import { fetchTreeEntries } from '$lib/loader/repo'
import { requestGraphQL } from '$lib/web'

import type { LayoutLoad } from './$types'

export const load: LayoutLoad = ({ parent, params }) => ({
    treeEntries: {
        deferred: parent().then(({ resolvedRevision, repoName, revision }) =>
            !isErrorLike(resolvedRevision)
                ? fetchTreeEntries({
                      repoName,
                      commitID: resolvedRevision.commitID,
                      revision: revision ?? '',
                      filePath: params.path ? dirname(params.path) : '.',
                      first: 2500,
                      requestGraphQL: options => requestGraphQL(options.request, options.variables),
                  })
                      .pipe(catchError((error): [ErrorLike] => [asError(error)]))
                      .toPromise()
                : null
        ),
    },
})
