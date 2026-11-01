# Nạp .env nếu có, để chạy trên máy cho tiện.
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: run test ctl upload-logos

run:
	go run ./cmd/server

test:
	go test ./...

# Ví dụ: make ctl ARGS="list"
ctl:
	go run ./cmd/tingctl $(ARGS)

# Tải logo trong assets/logos lên bucket R2 public. Cần: npx wrangler login. Ví dụ: make upload-logos BUCKET=ting-logos
upload-logos:
	@test -n "$(BUCKET)" || (echo "Cần BUCKET=<tên bucket R2>"; exit 1)
	for f in assets/logos/*.png; do \
		npx wrangler r2 object put "$(BUCKET)/logos/$$(basename $$f)" --file "$$f" --remote \
			--content-type image/png --cache-control "public, max-age=604800"; \
	done
