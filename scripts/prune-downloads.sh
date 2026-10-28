#!/usr/bin/env bash
# Removes from the R2 bucket behind dl.godwit.email the installers, update
# archives and delta updates of all but the newest KEEP versions (default 3).
# `mygo build -upload` only ever adds files, so every release stays otherwise.
# Kept besides: every version an update-<target>.json names, as its version or
# among its previous ones, whose archives the next build makes deltas from
# (updates.deltas in mygo.config.ts, 3 by default). An old installer's link is
# a 404 then: godwit.email/download/<target> is the one to give out.
# Nothing else is touched: the manifests, install.sh, plugins/.
#
# With DRY_RUN=1 it only lists what it would remove. Needs the aws CLI and jq,
# with the R2 token in AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY.
set -euo pipefail

keep=${KEEP:-3}
bucket=godwit-downloads # updates.s3 in mygo.config.ts
export AWS_ENDPOINT_URL=https://9c578ed9cad837fb4a2564db7b36b483.r2.cloudflarestorage.com
export AWS_DEFAULT_REGION=auto

# debVersion is mygo's (site/src/download.ts has it too): "1.2.0-rc-1" is "1.2.0~rc.1".
deb_version() {
	local v=${1#v} pre=
	if [[ $v == *-* ]]; then pre=${v#*-}; v="${v%%-*}~${pre//-/.}"; fi
	printf '%s\n' "${v//+/\~}"
}

# version KEY: the app version a file of a release is of, nothing for any other file.
version() {
	local re_target='(darwin|windows|linux)-(amd64|arm64)'
	if [[ $1 =~ ^godwit-(.+)-to-(.+)-${re_target}\.delta$ ]]; then echo "${BASH_REMATCH[2]}"
	elif [[ $1 =~ ^godwit-(.+)-${re_target}\.tar\.gz$ ]]; then echo "${BASH_REMATCH[1]}"
	elif [[ $1 =~ ^Godwit\ (Setup\ )?([^ ]+)(\ (amd64|arm64))?\.(dmg|exe)$ ]]; then echo "${BASH_REMATCH[2]}"
	elif [[ $1 =~ ^godwit_([^_]+)_(amd64|arm64)\.deb$ ]]; then echo "deb:${BASH_REMATCH[1]}"
	fi
}

listing=$(aws s3api list-objects-v2 --bucket "$bucket" --query 'Contents[].[Key,Size]' --output text)

# The versions are those of the update archives, the newest last.
versions=$(while IFS=$'\t' read -r key _; do
	if [[ $key =~ ^godwit-(.+)-(darwin|windows|linux)-(amd64|arm64)\.tar\.gz$ ]]; then echo "${BASH_REMATCH[1]}"; fi
done <<<"$listing" | sort -uV)
kept=$(tail -n "$keep" <<<"$versions")
for manifest in $(grep -oE '^update-[a-z0-9-]+\.json' <<<"$listing" || true); do
	kept+=$'\n'$(aws s3 cp "s3://$bucket/$manifest" - | jq -r '.version, (.previous // [])[].version')
done
kept=$(sort -uV <<<"$kept" | sed '/^$/d')
echo "Keeping $(echo $kept) of $(wc -l <<<"$versions" | tr -d ' ') versions"

declare -A keep_set
while read -r v; do keep_set[$v]=1 keep_set[deb:$(deb_version "$v")]=1; done <<<"$kept"

doomed=() bytes=0
while IFS=$'\t' read -r key size; do
	v=$(version "$key")
	[[ -n $v && -z ${keep_set[$v]:-} ]] || continue
	doomed+=("$key") bytes=$((bytes + size))
	echo "${DRY_RUN:+would remove }$key"
done <<<"$listing"
echo "${#doomed[@]} files, $((bytes / 1000000)) MB${DRY_RUN:+ (dry run: nothing removed)}"
[[ -z ${DRY_RUN:-} && ${#doomed[@]} -gt 0 ]] || exit 0

# delete-objects takes up to 1000 keys a call.
for ((i = 0; i < ${#doomed[@]}; i += 1000)); do
	printf '%s\n' "${doomed[@]:i:1000}" | jq -Rn '{Objects: [inputs | {Key: .}], Quiet: true}' >"${RUNNER_TEMP:-/tmp}/delete.json"
	errors=$(aws s3api delete-objects --bucket "$bucket" --delete "file://${RUNNER_TEMP:-/tmp}/delete.json" --query 'Errors' --output json)
	[[ $errors == null || $errors == "[]" ]] || { echo "$errors" >&2; exit 1; }
done
echo "Removed ${#doomed[@]} files"
