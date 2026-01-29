function statusApp() {
    return {
        socket: null,
        monitors: [],
        overallStatus: 2, // 0: Down, 1: Up, 2: Loading
        lastUpdated: '-',
        searchText: '',
        filterBy: 'None',
        sortBy: 'Name',
        now: Date.now(),

        init() {
            this.socket = io({
                transports: ['websocket', 'polling']
            });

            // Update time every second for dynamic relative time
            setInterval(() => {
                this.now = Date.now();
            }, 1000);

            this.socket.on('connect', () => {
                console.log('Connected to server (status page)');
                this.socket.emit('getMonitorList');
            });

            this.socket.on('monitorList', (list) => {
                if (!Array.isArray(list)) {
                    list = Object.values(list);
                }
                this.monitors = list.map(m => ({
                    ...m,
                    history: [], // Start with empty history
                    lastDuration: 0,
                    firstCheck: null,
                }));

                // Fetch history for each monitor
                this.monitors.forEach(m => {
                    this.socket.emit('getHeartbeatList', m.id);
                });

                this.updateOverallStatus();
                this.lastUpdated = new Date().toLocaleTimeString();
            });

            this.socket.on('heartbeatList', (monitorID, heartbeats) => {
                let m = this.monitors.find(x => x.id === monitorID);
                if (m) {
                    // Server returns heartbeats in desc order (newest first)
                    // We need to reverse so newest is at the end (displayed on the right)
                    const reversedHeartbeats = [...heartbeats].reverse();

                    // Store both status and duration
                    m.history = reversedHeartbeats.map(h => ({
                        status: h.status === 1,
                        duration: h.duration
                    }));

                    if (heartbeats.length > 0) {
                        // heartbeats[0] is the newest record (desc order from server)
                        m.lastDuration = heartbeats[0].duration;
                        // After reversing, firstCheck is the oldest, which is now at index 0 of reversedHeartbeats
                        m.firstCheck = reversedHeartbeats[0].time;
                    }

                    // Ensure we have exactly 30 items for the UI
                    // Pad with nulls at the beginning (left side) if less than 30
                    while (m.history.length < 30) {
                        m.history.unshift({ status: null, duration: 0 });
                    }
                    // Keep only the last 30 (newest 30 records, displayed on the right)
                    if (m.history.length > 30) {
                        m.history = m.history.slice(-30);
                    }
                }
            });

            this.socket.on('heartbeat', (hb) => {
                let m = this.monitors.find(x => x.id === hb.monitorID);
                if (m) {
                    m.status = hb.status;
                    m.msg = hb.msg;
                    m.lastDuration = hb.duration;
                    m.last_check = hb.time;

                    // Add to history
                    if (m.history) {
                        m.history.push({
                            status: hb.status === 1,
                            duration: hb.duration
                        });
                        if (m.history.length > 30) m.history.shift();
                    }
                    this.updateOverallStatus();
                    this.lastUpdated = new Date().toLocaleTimeString();
                }
            });

            // Periodically refresh list to be safe
            setInterval(() => {
                this.socket.emit('getMonitorList');
            }, 60000);
        },

        updateOverallStatus() {
            if (this.monitors.length === 0) {
                this.overallStatus = 2;
                return;
            }
            const allUp = this.monitors.every(m => m.status === 1);
            this.overallStatus = allUp ? 1 : 0;
        },

        get filteredMonitors() {
            let result = [...this.monitors];

            // Search
            if (this.searchText) {
                const search = this.searchText.toLowerCase();
                result = result.filter(m =>
                    m.name.toLowerCase().includes(search)
                );
            }

            // Filter
            if (this.filterBy !== 'None') {
                result = result.filter(m => m.type.toUpperCase() === this.filterBy.toUpperCase());
            }

            // Sort
            if (this.sortBy === 'Name') {
                result.sort((a, b) => a.name.localeCompare(b.name));
            } else if (this.sortBy === 'Status') {
                result.sort((a, b) => b.status - a.status);
            }

            return result;
        },

        get monitorTypes() {
            const types = new Set(this.monitors.map(m => m.type.toUpperCase()));
            return ['None', ...Array.from(types)];
        },

        get overallStatusText() {
            if (this.overallStatus === 2) return '正在获取状态...';
            return this.overallStatus === 1 ? '所有系统运行正常' : '部分服务出现异常';
        },

        get overallStatusSubtext() {
            if (this.overallStatus === 2) return '正在连接监控服务...';
            return this.overallStatus === 1
                ? '所有监控中的服务均响应正常。'
                : '一个或多个服务目前正经历访问问题。';
        },

        statusLabel(status) {
            switch (status) {
                case 1: return '健康';
                case 0: return '异常';
                case 2: return '检查中';
                default: return '未知';
            }
        },

        formatRelativeTime(dateStr) {
            if (!dateStr) return '';
            const date = new Date(dateStr);
            const now = new Date(this.now);
            const diffMs = now - date;
            const diffSec = Math.max(0, Math.round(diffMs / 1000));
            const diffMin = Math.round(diffSec / 60);
            const diffHour = Math.round(diffMin / 60);
            const diffDay = Math.round(diffHour / 24);

            if (diffSec < 60) return `${diffSec} 秒前`;
            if (diffMin < 60) return `${diffMin} 分钟前`;
            if (diffHour < 24) return `${diffHour} 小时前`;
            return `${diffDay} 天前`;
        }
    }
}
