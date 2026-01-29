function app() {
    return {
        socket: null,
        page: 'loading',
        monitors: [],
        currentMonitor: null,
        dashboardView: 'overview', // 'overview', 'details' or 'form'
        heartbeats: [],
        showAdvanced: false,
        isEditing: false,
        isEditingNotif: false,
        searchText: '',
        setupForm: { username: '', password: '', confirmPassword: '' },
        chart: null,
        chartInitTimeout: null,
        // 图表视图模式：'recent'（最近30条原始数据）, '24h'（24小时聚合）, '7d'（7天聚合）
        chartView: 'recent',
        // 图表聚合数据缓存
        chartAggregatedData: null,
        monitorStats: {
            uptime1h: 100,
            uptime24h: 100,
            uptime7d: 100,
            avgResponse24h: 0
        },

        loginForm: { username: '', password: '' },
        monitorForm: {
            name: '',
            url: '',
            type: 'http',
            interval: 60,
            method: 'GET',
            timeout: 10,
            expected_status: 0,
            follow_redirects: true,
            headers: '',
            body: '',
            response_regex: ''
        },

        get filteredMonitors() {
            if (!this.searchText) return this.monitors;
            const search = this.searchText.toLowerCase();
            return this.monitors.filter(m =>
                m.name.toLowerCase().includes(search) ||
                m.url.toLowerCase().includes(search)
            );
        },

        get globalStats() {
            return {
                total: this.monitors.length,
                up: this.monitors.filter(m => m.active === 1 && m.status === 1).length,
                down: this.monitors.filter(m => m.active === 1 && m.status === 0).length,
                paused: this.monitors.filter(m => m.active === 0).length,
                pending: this.monitors.filter(m => m.active === 1 && (m.status === 2 || m.status === undefined)).length
            };
        },

        notifications: [],
        notifForm: {
            type: 'trigger', // 'trigger' or 'schedule'
            name: '',
            monitorName: '*',
            onStatus: 'down',
            email: '',
            time: '09:00',
            days: []
        },
        showNotifModal: false,

        // Modal State
        msgBox: {
            show: false,
            type: 'info', // info, success, error, warning, confirm
            title: '',
            message: '',
            confirmText: '确定',
            isDestructive: false,
            onConfirm: null
        },

        get parsedNotifications() {
            return this.notifications.map(n => {
                let cfg = {};
                try { cfg = JSON.parse(n.config); } catch (e) { }
                return { ...n, cfg };
            });
        },

        get triggerNotifications() {
            return this.parsedNotifications.filter(n => n.type === 'trigger');
        },

        get scheduledNotifications() {
            return this.parsedNotifications.filter(n => n.type === 'schedule');
        },

        get avgResponseTime() {
            if (this.heartbeats.length === 0) return 0;
            const validHeartbeats = this.heartbeats.filter(h => h.duration !== undefined);
            if (validHeartbeats.length === 0) return 0;
            const sum = validHeartbeats.reduce((acc, h) => acc + h.duration, 0);
            return Math.round(sum / validHeartbeats.length);
        },

        init() {
            this.socket = io({
                transports: ['websocket', 'polling']
            });

            this.socket.on('connect', () => {
                console.log('已连接到服务器');
                this.socket.emit('checkSetup', (res) => {
                    if (res.needSetup) {
                        this.page = 'setup';
                    } else {
                        const token = localStorage.getItem('pinggo_token');
                        if (token) {
                            this.socket.emit('auth', { token }, (authRes) => {
                                if (authRes && authRes.ok) {
                                    this.page = 'dashboard';
                                    this.socket.emit('getMonitorList');
                                } else {
                                    localStorage.removeItem('pinggo_token');
                                    this.page = 'login';
                                }
                            });
                        } else {
                            if (this.page === 'loading') this.page = 'login';
                        }
                    }
                });
            });

            this.socket.on('adminMonitorList', (list) => {
                if (!Array.isArray(list)) {
                    list = Object.values(list);
                }
                this.monitors = list.sort((a, b) => (b.active - a.active) || (a.id - b.id));
            });

            this.socket.on('monitor', (m) => {
                let index = this.monitors.findIndex(x => x.id === m.id);
                if (index !== -1) {
                    this.monitors[index] = m;
                } else {
                    this.monitors.push(m);
                }
                if (this.currentMonitor && this.currentMonitor.id === m.id) {
                    this.currentMonitor = m;
                }
            });

            // 处理心跳事件 - 动态更新侧边栏、统计信息和图表
            this.socket.on('heartbeat', (hb) => {
                // 查找并更新对应的 monitor
                let monitorIndex = this.monitors.findIndex(x => x.id === hb.monitorID);
                if (monitorIndex !== -1) {
                    // 获取原始 monitor 引用
                    let m = this.monitors[monitorIndex];

                    // 更新状态和消息
                    m.status = hb.status;
                    m.msg = hb.msg;

                    // 更新最近检查结果 (Uptime History条纹图)
                    if (!m.recentResults) m.recentResults = Array(30).fill(-1);
                    m.recentResults.push(hb.status);
                    if (m.recentResults.length > 30) {
                        m.recentResults.shift();
                    }

                    // 触发 Alpine.js 响应式更新 - 通过重新赋值整个对象
                    // 这确保侧边栏的条纹图会刷新
                    this.monitors[monitorIndex] = { ...m };
                }

                // 如果是当前选中的监控项，更新详情页
                if (this.currentMonitor && this.currentMonitor.id === hb.monitorID) {
                    // 更新心跳列表（最近事件）
                    this.heartbeats.unshift(hb);
                    if (this.heartbeats.length > 30) this.heartbeats.pop();

                    // 更新当前监控状态
                    this.currentMonitor.status = hb.status;
                    this.currentMonitor.msg = hb.msg;

                    // 同步更新 recentResults 到 currentMonitor
                    if (monitorIndex !== -1) {
                        this.currentMonitor.recentResults = [...this.monitors[monitorIndex].recentResults];
                    }

                    // 刷新统计信息（uptime1h, uptime24h, uptime7d, avgResponse24h）
                    this.socket.emit('getMonitorStats', this.currentMonitor.id);

                    // 更新图表
                    this.updateChart();
                }
            });

            this.socket.on('heartbeatList', (monitorID, list) => {
                if (this.currentMonitor && this.currentMonitor.id === monitorID) {
                    this.heartbeats = list;
                    // Cancel any pending chart initialization
                    if (this.chartInitTimeout) {
                        clearTimeout(this.chartInitTimeout);
                    }
                    // Use setTimeout to ensure DOM is fully rendered
                    this.$nextTick(() => {
                        this.chartInitTimeout = setTimeout(() => {
                            // Double-check the monitor is still selected
                            if (this.currentMonitor && this.currentMonitor.id === monitorID) {
                                this.initChart();
                            }
                        }, 100);
                    });
                }
            });

            this.socket.on('monitorStats', (monitorID, stats) => {
                // 只在当前选中的监控项匹配时更新统计
                if (this.currentMonitor && this.currentMonitor.id === monitorID) {
                    this.monitorStats = {
                        uptime1h: Math.round(stats.uptime1h || 100),
                        uptime24h: Math.round(stats.uptime24h || 100),
                        uptime7d: Math.round(stats.uptime7d || 100),
                        avgResponse24h: Math.round(stats.avgResponse24h || 0)
                    };
                }
            });

            // 处理图表聚合数据（24h/7d 视图）
            this.socket.on('chartData', (monitorID, result) => {
                if (this.currentMonitor && this.currentMonitor.id === monitorID) {
                    this.chartAggregatedData = result.data;
                    // 使用聚合数据更新图表
                    this.updateChartWithAggregatedData();
                }
            });

            this.socket.on('updateMonitorList', () => {
                this.socket.emit('getMonitorList');
            });

            this.socket.on('notificationList', (list) => {
                this.notifications = list || [];
            });
        },

        openNotifications() {
            this.dashboardView = 'notifications';
            this.socket.emit('getNotificationList'); // Ensure we have latest
        },

        openAddTrigger() {
            this.isEditingNotif = false;
            this.notifForm = {
                id: null,
                type: 'trigger',
                name: '',
                monitorName: '*',
                onStatus: 'down',
                email: '',
                time: '',
                days: []
            };
            this.showNotifModal = true;
        },

        openAddSchedule() {
            this.isEditingNotif = false;
            this.notifForm = {
                id: null,
                type: 'schedule',
                name: '',
                monitorName: '',
                onStatus: '',
                email: '',
                time: '09:00',
                days: []
            };
            this.showNotifModal = true;
        },

        openEditNotification(n) {
            this.isEditingNotif = true;
            let cfg = {};
            try { cfg = JSON.parse(n.config); } catch (e) { }
            this.notifForm = {
                id: n.id,
                type: n.type,
                name: n.name,
                monitorName: cfg.monitor_name || '*',
                onStatus: cfg.on_status || 'down',
                email: cfg.email || '',
                time: cfg.time || '09:00',
                days: cfg.days || []
            };
            this.showNotifModal = true;
        },

        saveNotification() {
            // Basic validation
            if (!this.notifForm.email) {
                this.showAlert('表单错误', '请输入接收邮箱', 'warning');
                return;
            }
            // Default name if empty
            if (!this.notifForm.name) {
                this.notifForm.name = this.notifForm.type === 'trigger' ? '监控告警' : '每日日报';
            }

            const isTrigger = this.notifForm.type === 'trigger';

            // Explicitly construct payload to avoid spread syntax issues and ensure correct keys for Go backend
            const payload = {
                id: this.notifForm.id,
                name: this.notifForm.name,
                type: this.notifForm.type,
                email: this.notifForm.email,
                monitor_name: isTrigger ? (this.notifForm.monitorName || '*') : '',
                on_status: isTrigger ? (this.notifForm.onStatus || 'down') : '',
                time: isTrigger ? '' : (this.notifForm.time || '09:00')
            };

            const event = this.isEditingNotif ? 'editNotification' : 'addNotification';
            this.socket.emit(event, payload, (res) => {
                if (res && res.ok) {
                    this.showNotifModal = false;
                    this.socket.emit('getNotificationList'); // Refresh list
                } else {
                    this.showAlert('保存失败', res ? res.msg : '未知错误', 'error');
                }
            });
        },

        async toggleNotification(n) {
            this.socket.emit('toggleNotification', n.id);
        },

        deleteNotification(id) {
            this.showConfirm('删除通知', '确定删除此通知配置吗？此操作无法撤销。', () => {
                this.socket.emit('deleteNotification', id);
            }, true, '删除');
        },

        doSetup() {
            if (this.setupForm.password !== this.setupForm.confirmPassword) {
                this.showAlert('密码错误', '两次输入的密码不一致！', 'warning');
                return;
            }
            this.socket.emit('setup', this.setupForm, (res) => {
                if (res.ok) {
                    this.page = 'login';
                    this.showAlert('初始化成功', '系统初始化完成，请登录。', 'success');
                } else {
                    this.showAlert('初始化失败', res.msg || '未知错误', 'error');
                }
            });
        },

        doLogin() {
            this.socket.emit('login', this.loginForm, (res) => {
                if (res.ok) {
                    localStorage.setItem('pinggo_token', res.token);
                    this.page = 'dashboard';
                    this.socket.emit('getMonitorList');
                } else {
                    this.showAlert('登录失败', res.msg || '凭据无效', 'error');
                }
            });
        },

        logout() {
            this.socket.emit('logout', () => {
                localStorage.removeItem('pinggo_token');
                this.page = 'login';
                this.currentMonitor = null;
                this.loginForm.password = '';
            });
        },

        openAddMonitor() {
            this.destroyChart();
            this.isEditing = false;
            this.showAdvanced = false;
            this.dashboardView = 'form';
            this.currentMonitor = null;
            this.monitorForm = {
                name: '',
                url: '',
                type: 'http',
                interval: 60,
                method: 'GET',
                timeout: 10,
                expected_status: 0,
                follow_redirects: true,
                headers: '',
                body: '',
                response_regex: ''
            };
        },

        openEditMonitor(m) {
            this.destroyChart();
            // Fetch full details (including URL) from server
            this.socket.emit('getMonitor', m.id);
            // We need to wait for the 'monitor' event to populate the form
            // So we set a temporary flag or listener
            const onMonitorData = (data) => {
                if (data.id === m.id) {
                    this.isEditing = true;
                    this.showAdvanced = false;
                    this.dashboardView = 'form';
                    this.monitorForm = {
                        id: data.id,
                        name: data.name,
                        url: data.url, // Now available from secure fetch
                        type: data.type,
                        interval: data.interval,
                        method: data.method || 'GET',
                        timeout: data.timeout || 10,
                        expected_status: data.expected_status || 0,
                        follow_redirects: data.follow_redirects !== undefined ? data.follow_redirects : true,
                        headers: data.headers || '',
                        body: data.body || '',
                        response_regex: data.response_regex || ''
                    };
                    this.socket.off('monitor', onMonitorData); // Clean up listener
                }
            };
            this.socket.on('monitor', onMonitorData);
        },

        cloneMonitor(m) {
            this.destroyChart();
            // Fetch full details (including URL) from server
            this.socket.emit('getMonitor', m.id);
            const onMonitorData = (data) => {
                if (data.id === m.id) {
                    this.isEditing = false;
                    this.showAdvanced = false;
                    this.dashboardView = 'form';
                    this.monitorForm = {
                        name: data.name + ' (Copy)',
                        url: data.url,
                        type: data.type,
                        interval: data.interval,
                        method: data.method || 'GET',
                        timeout: data.timeout || 10,
                        expected_status: data.expected_status || 0,
                        follow_redirects: data.follow_redirects !== undefined ? data.follow_redirects : true,
                        headers: data.headers || '',
                        body: data.body || '',
                        response_regex: data.response_regex || ''
                    };
                    this.socket.off('monitor', onMonitorData);
                }
            };
            this.socket.on('monitor', onMonitorData);
        },

        togglePause(m) {
            const newActive = m.active ? 0 : 1;
            this.socket.emit('edit', { ...m, active: newActive }, (res) => {
                if (res.ok) {
                    m.active = newActive;
                    this.socket.emit('getMonitorList');
                }
            });
        },

        // Safely destroy chart and reset canvas to avoid context issues
        destroyChart() {
            if (this.chartInitTimeout) {
                clearTimeout(this.chartInitTimeout);
                this.chartInitTimeout = null;
            }

            if (this.chart) {
                try {
                    this.chart.stop();
                    this.chart.destroy();
                } catch (e) {
                    console.warn('Chart cleanup warning:', e);
                }
                this.chart = null;
            }

            // Brute force: Recreate the canvas element to ensure a clean slate
            const wrapper = document.getElementById('chartWrapper');
            if (wrapper) {
                wrapper.innerHTML = '<canvas id="responseTimeChart"></canvas>';
            }
        },

        selectMonitor(m) {
            // Safely destroy existing chart before switching
            this.destroyChart();

            this.currentMonitor = m;
            this.dashboardView = 'details';
            this.heartbeats = [];
            this.monitorStats = { uptime1h: 100, uptime24h: 100, uptime7d: 100, avgResponse24h: 0 };
            // 重置图表视图为默认的最近数据
            this.chartView = 'recent';
            this.chartAggregatedData = null;
            this.socket.emit('getHeartbeatList', m.id);
            this.socket.emit('getMonitorStats', m.id);
        },

        showOverview() {
            this.destroyChart();
            this.currentMonitor = null;
            this.dashboardView = 'overview';
        },

        saveMonitor() {
            const event = this.isEditing ? 'edit' : 'add';
            this.socket.emit(event, this.monitorForm, (res) => {
                if (res.ok) {
                    this.dashboardView = 'details';
                    this.socket.emit('getMonitorList');
                    if (this.isEditing && this.currentMonitor && this.currentMonitor.id === this.monitorForm.id) {
                        Object.assign(this.currentMonitor, this.monitorForm);
                    }
                } else {
                    this.showAlert('保存失败', res.msg, 'error');
                }
            });
        },

        deleteMonitor(id) {
            this.showConfirm('删除监控项', '确定要删除这个监控项吗？相关的历史数据也将被清除。', () => {
                this.socket.emit('deleteMonitor', id, (res) => {
                    if (res.ok) {
                        this.currentMonitor = null;
                        this.socket.emit('getMonitorList');
                    } else {
                        this.showAlert('操作失败', '删除失败', 'error');
                    }
                });
            }, true, '删除');
        },

        clearLogs() {
            if (!this.currentMonitor) return;
            this.showConfirm('清除数据', '确定要清除该监控项的所有历史数据吗？此操作不可逆。', () => {
                this.socket.emit('clearEvents', this.currentMonitor.id, (res) => {
                    if (res && res.ok) {
                        this.heartbeats = [];
                        this.monitorStats = { uptime1h: 100, uptime24h: 100, uptime7d: 100, avgResponse24h: 0 };
                        this.chartAggregatedData = null;
                        this.updateChart();
                        this.showAlert('操作成功', '数据已清除', 'success');
                    } else {
                        this.showAlert('清除失败', res.msg || "未知错误", 'error');
                    }
                });
            }, true, '清除');
        },

        statusClass(status, active = 1) {
            if (active === 0) return 'bg-gray-400';
            switch (status) {
                case 1: return 'bg-emerald-500'; // UP
                case 0: return 'bg-rose-500'; // DOWN
                case 2: return 'bg-amber-500'; // PENDING
                case 3: return 'bg-blue-500'; // MAINTENANCE
                default: return 'bg-slate-400';
            }
        },

        statusText(status, active = 1) {
            if (active === 0) return '已暂停';
            switch (status) {
                case 1: return '正常';
                case 0: return '中断';
                case 2: return '检查中';
                case 3: return '维护中';
                default: return '未知';
            }
        },

        statusTextClass(status, active = 1) {
            if (active === 0) return 'text-gray-500';
            switch (status) {
                case 1: return 'text-emerald-600';
                case 0: return 'text-rose-600';
                case 2: return 'text-amber-600';
                case 3: return 'text-blue-600';
                default: return 'text-slate-500';
            }
        },

        formatDuration(seconds) {
            if (!seconds) return '0秒';
            if (seconds < 60) return seconds + '秒';
            const m = Math.floor(seconds / 60);
            const s = seconds % 60;
            return s > 0 ? `${m}分${s}秒` : `${m}分钟`;
        },

        initChart() {
            // First destroy the old chart and recreate the canvas DOM element
            this.destroyChart();

            // Get the fresh canvas element and its context
            const canvas = document.getElementById('responseTimeChart');
            if (!canvas) {
                console.warn('Canvas element not found after recreation');
                return;
            }

            // Check visibility
            if (!canvas.offsetParent) {
                // If not visible, we can't reliably init Chart.js
                return;
            }

            const ctx = canvas.getContext('2d');
            if (!ctx) {
                console.warn('Failed to get canvas context');
                return;
            }

            const labels = [...this.heartbeats].map(h => h.time).reverse();
            const data = [...this.heartbeats].map(h => h.duration || 0).reverse();

            try {
                this.chart = new Chart(canvas, {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: [{
                            label: '响应时间 (ms)',
                            data: data,
                            borderColor: '#2ecc71',
                            backgroundColor: 'rgba(46, 204, 113, 0.1)',
                            borderWidth: 2,
                            fill: true,
                            tension: 0.4,
                            pointRadius: 0,
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        // Disable animations to prevent requestAnimationFrame issues
                        animation: false,
                        plugins: {
                            legend: { display: false }
                        },
                        scales: {
                            x: { display: false },
                            y: {
                                beginAtZero: true,
                                grid: { color: '#f0f0f0' },
                                ticks: { font: { size: 10 } }
                            }
                        }
                    }
                });
            } catch (e) {
                console.warn('Error creating chart:', e);
                this.chart = null;
            }
        },


        updateChart() {
            // 如果当前视图不是 'recent'，不需要用原始数据更新
            if (this.chartView !== 'recent') {
                return;
            }

            if (!this.chart) {
                // If chart doesn't exist, try to initialize it
                this.$nextTick(() => this.initChart());
                return;
            }

            // Check if chart canvas is still connected
            if (!this.chart.canvas || !this.chart.canvas.isConnected) {
                console.warn('Chart canvas disconnected, reinitializing...');
                this.destroyChart();
                this.$nextTick(() => this.initChart());
                return;
            }

            try {
                const labels = [...this.heartbeats].map(h => h.time).reverse();
                const data = [...this.heartbeats].map(h => h.duration || 0).reverse();
                this.chart.data.labels = labels;
                this.chart.data.datasets[0].data = data;
                // Use 'none' mode to skip animation during update
                this.chart.update('none');
            } catch (e) {
                console.warn('Error updating chart:', e);
                // Try to reinitialize the chart
                this.destroyChart();
                this.$nextTick(() => this.initChart());
            }
        },

        // 切换图表视图：'recent'（最近30条）, '24h'（24小时聚合）, '7d'（7天聚合）
        switchChartView(view) {
            if (!this.currentMonitor) return;

            this.chartView = view;

            if (view === 'recent') {
                // 切换回最近数据视图，使用已有的heartbeats数据
                this.chartAggregatedData = null;
                this.$nextTick(() => this.initChart());
            } else {
                // 请求聚合数据（24h 或 7d）
                this.socket.emit('getChartData', this.currentMonitor.id, view);
            }
        },

        // 使用聚合数据更新图表（24h/7d 视图）
        updateChartWithAggregatedData() {
            if (!this.chartAggregatedData || this.chartAggregatedData.length === 0) {
                return;
            }

            // 销毁旧图表并重新创建canvas
            this.destroyChart();

            const canvas = document.getElementById('responseTimeChart');
            if (!canvas) {
                console.warn('Canvas element not found');
                return;
            }

            if (!canvas.offsetParent) {
                return;
            }

            const ctx = canvas.getContext('2d');
            if (!ctx) {
                console.warn('Failed to get canvas context');
                return;
            }

            // 从聚合数据构建图表数据
            const labels = this.chartAggregatedData.map(p => p.time);
            const data = this.chartAggregatedData.map(p => p.duration);

            // 根据状态设置点的颜色（可选：用于显示异常点）
            const pointColors = this.chartAggregatedData.map(p => {
                if (p.status === 0) return '#e74c3c'; // 异常：红色
                if (p.status === -1) return '#bdc3c7'; // 无数据：灰色
                if (p.isLive) return '#3498db'; // 实时数据：蓝色
                return '#2ecc71'; // 正常：绿色
            });

            try {
                this.chart = new Chart(canvas, {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: [{
                            label: '响应时间 (ms)',
                            data: data,
                            borderColor: '#2ecc71',
                            backgroundColor: 'rgba(46, 204, 113, 0.1)',
                            borderWidth: 2,
                            fill: true,
                            tension: 0.4,
                            pointRadius: 3,
                            pointBackgroundColor: pointColors,
                            pointBorderColor: pointColors,
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: false,
                        plugins: {
                            legend: { display: false },
                            tooltip: {
                                callbacks: {
                                    // 自定义tooltip显示更多信息
                                    afterLabel: (context) => {
                                        const point = this.chartAggregatedData[context.dataIndex];
                                        if (point) {
                                            let lines = [];
                                            if (point.uptime !== undefined) {
                                                lines.push(`可用率: ${point.uptime.toFixed(1)}%`);
                                            }
                                            if (point.isLive) {
                                                lines.push('(实时数据)');
                                            }
                                            return lines;
                                        }
                                        return '';
                                    }
                                }
                            }
                        },
                        scales: {
                            x: {
                                display: true,
                                ticks: {
                                    font: { size: 9 },
                                    maxRotation: 45,
                                    minRotation: 0,
                                    // 只显示部分标签避免拥挤
                                    callback: function (value, index, values) {
                                        // 每隔几个显示一个标签
                                        const step = Math.ceil(values.length / 8);
                                        return index % step === 0 ? this.getLabelForValue(value) : '';
                                    }
                                }
                            },
                            y: {
                                beginAtZero: true,
                                grid: { color: '#f0f0f0' },
                                ticks: { font: { size: 10 } }
                            }
                        }
                    }
                });
            } catch (e) {
                console.warn('Error creating aggregated chart:', e);
                this.chart = null;
            }
        },

        // Helper Methods for Custom Modal
        showAlert(title, message, type = 'info') {
            this.msgBox = {
                show: true,
                type: type,
                title: title,
                message: message,
                confirmText: '确定',
                isDestructive: false,
                onConfirm: null
            };
        },

        showConfirm(title, message, onConfirm, isDestructive = false, confirmText = '确定') {
            this.msgBox = {
                show: true,
                type: 'confirm',
                title: title,
                message: message,
                confirmText: confirmText,
                isDestructive: isDestructive,
                onConfirm: onConfirm
            };
        },

        closeMsgBox() {
            this.msgBox.show = false;
            // cleanup after transition
            setTimeout(() => {
                this.msgBox.onConfirm = null;
            }, 300);
        },

        confirmMsgBox() {
            if (this.msgBox.onConfirm) {
                this.msgBox.onConfirm();
            }
            this.closeMsgBox();
        }
    }
}