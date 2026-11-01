#!/usr/bin/env bash
# Chọn phiên bản để deploy khi bấm Run workflow.
#   deploy-version.sh <ref_type> <ref_name> <version nhập tay, có thể rỗng>
# Chạy từ một tag: để trống thì lấy tên tag; nhập khác tên tag thì báo lỗi.
# Chạy từ nhánh (ví dụ main): bắt buộc nhập.
# In ra phiên bản đã chọn, dạng v1.2.3.
set -euo pipefail

ref_type=$1
ref_name=$2
input=${3:-}

if [[ $ref_type == tag ]]; then
  if [[ -z $input ]]; then
    version=$ref_name
  elif [[ $input != "$ref_name" ]]; then
    echo "::error::Chạy từ tag $ref_name nhưng version nhập là $input. Để trống hoặc nhập đúng $ref_name." >&2
    exit 1
  else
    version=$input
  fi
else
  if [[ -z $input ]]; then
    echo "::error::Chạy từ nhánh $ref_name thì phải nhập version, ví dụ v1.2.3. Hoặc chọn tag ở ô Use workflow from." >&2
    exit 1
  fi
  version=$input
fi

if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "::error::version phải có dạng v1.2.3, nhận được: $version" >&2
  exit 1
fi
echo "$version"
