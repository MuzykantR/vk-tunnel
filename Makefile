.PHONY: build-server setup-bot systemd-install restart restart-server restart-bot deploy deploy-server deploy-daemon deploy-bot logs-daemon logs-bot wrap wrap-clear wrap-show

# Путь к проекту на сервере
DIR = /opt/vk-vpn

# Drop-in для оверрайда env'ов сервиса без правки основного unit-файла.
# Удобно для toggle-флагов вроде VK_VPN_TURN_WRAP без `git diff`.
WRAP_DROPIN_DIR  = /etc/systemd/system/vk-vpn-daemon.service.d
WRAP_DROPIN_FILE = $(WRAP_DROPIN_DIR)/wrap.conf

build-server:
	@echo "Building Go daemon..."
	cd vk-vpn-server && go build -o vk-vpn-daemon main.go
	chmod +x vk-vpn-server/vk-vpn-daemon
	@echo "Build complete."

setup-bot:
	@echo "Setting up Python bot..."
	cd vk-vpn-bot && \
	if [ ! -d "venv" ]; then python3 -m venv venv; fi && \
	. venv/bin/activate && \
	pip install --upgrade pip && \
	pip install -r requirements.txt
	@echo "Bot setup complete."

systemd-install:
	@echo "Installing systemd services..."
	sudo cp systemd/vk-vpn-daemon.service /etc/systemd/system/
	sudo cp systemd/vk-vpn-bot.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable vk-vpn-daemon
	sudo systemctl enable vk-vpn-bot
	@echo "Systemd services installed and enabled."

restart:
	@echo "Restarting services..."
	sudo systemctl restart vk-vpn-daemon vk-vpn-bot
	@echo "Services restarted."

restart-server:
	@echo "Restarting VK VPN Daemon..."
	sudo systemctl restart vk-vpn-daemon
	@echo "Daemon restarted."

restart-bot:
	@echo "Restarting Telegram Bot..."
	sudo systemctl restart vk-vpn-bot
	@echo "Bot restarted."

deploy: build-server setup-bot restart
	@echo "Deploy successful."

deploy-server: build-server restart-server
	@echo "Server deploy successful."

deploy-daemon: deploy-server

deploy-bot: setup-bot restart-bot
	@echo "Bot deploy successful."

logs-daemon:
	@# Файл усекается при каждом старте процесса (restart/deploy) — `-F`
	@# переоткрывает его при truncate, так что хвост не «застревает».
	sudo tail -F /var/log/vk-vpn-daemon.log

logs-daemon-journal:
	sudo journalctl -u vk-vpn-daemon -f

logs-bot:
	sudo journalctl -u vk-vpn-bot -f

# Toggle protocol-aware WRAP obfuscation of the TURN ChannelData
# payload on the server. Default in the binary is ON, so the drop-in
# is only needed to *disable* (N=0) for A/B comparison. Using a
# systemd drop-in keeps the main unit file untouched / git-clean.
#
# Usage:
#   make wrap N=1     — explicit enable (writes VK_VPN_TURN_WRAP=1 drop-in)
#   make wrap N=0     — disable (writes VK_VPN_TURN_WRAP=0 drop-in)
#   make wrap-clear   — remove drop-in, restore binary default (= ON)
#   make wrap-show    — current state + recent log lines
#
# Client side (Windows) needs the same flag:
#   default behaviour (ON):  no env var needed
#   explicit disable:        PowerShell  $env:VK_VPN_TURN_WRAP = "0"
#                            cmd.exe     set VK_VPN_TURN_WRAP=0
wrap:
	@if [ -z "$(N)" ]; then \
		echo "Usage: make wrap N=1 (enable) or make wrap N=0 (disable)"; \
		echo "       make wrap-clear to remove drop-in and restore binary default (ON)"; \
		exit 1; \
	fi; \
	case "$(N)" in \
		1|0) \
			echo "Setting VK_VPN_TURN_WRAP=$(N) via systemd drop-in..."; \
			sudo mkdir -p $(WRAP_DROPIN_DIR); \
			printf '[Service]\nEnvironment=VK_VPN_TURN_WRAP=%s\n' "$(N)" \
				| sudo tee $(WRAP_DROPIN_FILE) > /dev/null; \
			sudo systemctl daemon-reload; \
			sudo systemctl restart vk-vpn-daemon; \
			echo "Done. Verify with: make wrap-show"; \
			;; \
		*) \
			echo "N must be 0 or 1, got: $(N)"; exit 1; \
			;; \
	esac

wrap-clear:
	@echo "Removing WRAP drop-in (binary default ON will take effect)..."
	@sudo rm -f $(WRAP_DROPIN_FILE)
	@sudo systemctl daemon-reload
	@sudo systemctl restart vk-vpn-daemon
	@echo "Done. Verify with: make wrap-show"

wrap-show:
	@echo "=== Drop-in file ==="
	@if [ -f $(WRAP_DROPIN_FILE) ]; then \
		echo "Path: $(WRAP_DROPIN_FILE)"; \
		sudo cat $(WRAP_DROPIN_FILE); \
	else \
		echo "(no drop-in — binary default applies: WRAP ON)"; \
	fi
	@echo
	@echo "=== Resolved unit Environment ==="
	@sudo systemctl show vk-vpn-daemon -p Environment \
		| sed 's/ /\n/g' | grep -E 'VK_VPN_TURN_WRAP|^Environment=' || \
		echo "(VK_VPN_TURN_WRAP not present in resolved env — binary default ON)"
	@echo
	@echo "=== Recent wrap log lines ==="
	@sudo journalctl -u vk-vpn-daemon -n 200 --no-pager \
		| grep -iE 'turn-wrap|VK_VPN_TURN_WRAP' | tail -10 || \
		echo "(no wrap log lines in last 200 journal entries)"
