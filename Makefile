.PHONY: build-server setup-bot systemd-install restart deploy logs-daemon logs-bot

# Путь к проекту на сервере
DIR = /opt/vk-vpn

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
	@sudo bash -c "cat << 'EOF' > /etc/systemd/system/vk-vpn-daemon.service\n\
[Unit]\n\
Description=VK WebRTC VPN Daemon\n\
After=network.target\n\
\n\
[Service]\n\
Type=simple\n\
User=root\n\
WorkingDirectory=$(DIR)/vk-vpn-server\n\
ExecStart=$(DIR)/vk-vpn-server/vk-vpn-daemon --cookies=cookies.json --port=8080\n\
Restart=always\n\
RestartSec=3\n\
TimeoutStopSec=2\n\
StandardOutput=journal\n\
StandardError=journal\n\
\n\
[Install]\n\
WantedBy=multi-user.target\n\
EOF"
	@sudo bash -c "cat << 'EOF' > /etc/systemd/system/vk-vpn-bot.service\n\
[Unit]\n\
Description=VK VPN Telegram Bot\n\
After=network.target vk-vpn-daemon.service\n\
\n\
[Service]\n\
Type=simple\n\
User=root\n\
WorkingDirectory=$(DIR)/vk-vpn-bot\n\
ExecStart=$(DIR)/vk-vpn-bot/venv/bin/python main.py\n\
Restart=always\n\
RestartSec=3\n\
StandardOutput=journal\n\
StandardError=journal\n\
\n\
[Install]\n\
WantedBy=multi-user.target\n\
EOF"
	sudo systemctl daemon-reload
	sudo systemctl enable vk-vpn-daemon
	sudo systemctl enable vk-vpn-bot
	@echo "Systemd services installed and enabled."

restart:
	@echo "Restarting services..."
	sudo systemctl restart vk-vpn-daemon vk-vpn-bot
	@echo "Services restarted."

deploy: build-server setup-bot restart
	@echo "Deploy successful."

logs-daemon:
	sudo journalctl -u vk-vpn-daemon -f

logs-bot:
	sudo journalctl -u vk-vpn-bot -f
