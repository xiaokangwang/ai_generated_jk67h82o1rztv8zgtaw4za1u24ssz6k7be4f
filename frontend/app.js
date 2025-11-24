// API Configuration
const API_BASE = window.location.origin;

// State Management
let currentUser = null;
let jwtToken = localStorage.getItem('jwt_token');

// DOM Elements
const loginSection = document.getElementById('login-section');
const dashboardSection = document.getElementById('dashboard-section');
const userName = document.getElementById('user-name');
const userBadge = document.getElementById('user-badge');
const slotCount = document.getElementById('slot-count');
const slotLimit = document.getElementById('slot-limit');
const logoutBtn = document.getElementById('logout-btn');
const allocateBtn = document.getElementById('allocate-btn');
const allocationStatus = document.getElementById('allocation-status');
const slotsContainer = document.getElementById('slots-container');
const loadingOverlay = document.getElementById('loading-overlay');

// Utility Functions
function showLoading() {
    loadingOverlay.style.display = 'flex';
}

function hideLoading() {
    loadingOverlay.style.display = 'none';
}

function showStatus(element, message, type) {
    element.textContent = message;
    element.className = `status-message ${type}`;
    setTimeout(() => {
        element.className = 'status-message';
    }, 5000);
}

async function apiCall(endpoint, options = {}) {
    const headers = {
        'Content-Type': 'application/json',
        ...options.headers
    };

    if (jwtToken) {
        headers['Authorization'] = `Bearer ${jwtToken}`;
    }

    const response = await fetch(`${API_BASE}${endpoint}`, {
        ...options,
        headers
    });

    if (!response.ok) {
        const text = await response.text();
        throw new Error(text || `HTTP ${response.status}`);
    }

    return response.json();
}

// Authentication
async function loadUserInfo() {
    try {
        showLoading();
        const data = await apiCall('/api/me');
        currentUser = data;

        userName.textContent = data.username;
        if (data.is_core_stargazer) {
            userBadge.style.display = 'inline-block';
        }

        await loadUserState();
        await loadSlots();

        loginSection.style.display = 'none';
        dashboardSection.style.display = 'block';
    } catch (error) {
        console.error('Failed to load user info:', error);
        logout();
    } finally {
        hideLoading();
    }
}

async function loadUserState() {
    try {
        const state = await apiCall('/api/state');
        const slots = state.associated_forwarder_server_slots || [];
        slotCount.textContent = slots.length;

        // Determine slot limit based on core stargazer status
        const limit = currentUser.is_core_stargazer ? 5 : 1;
        slotLimit.textContent = limit;

        // Disable allocate button if at limit
        if (slots.length >= limit) {
            allocateBtn.disabled = true;
            allocateBtn.textContent = 'Limit Reached';
        } else {
            allocateBtn.disabled = false;
            allocateBtn.textContent = 'Allocate Slot';
        }
    } catch (error) {
        console.error('Failed to load user state:', error);
    }
}

async function loadSlots() {
    try {
        const state = await apiCall('/api/state');
        const slots = state.associated_forwarder_server_slots || [];

        if (slots.length === 0) {
            slotsContainer.innerHTML = '<p class="empty-state">No slots allocated yet.</p>';
            return;
        }

        slotsContainer.innerHTML = slots.map(slotId => `
            <div class="slot-card" id="slot-${slotId}">
                <div class="slot-header">
                    <span class="slot-id">Slot #${slotId}</span>
                    <button class="btn btn-success" onclick="generateToken(${slotId})">Generate Token</button>
                </div>
                <div class="token-section" id="tokens-${slotId}" style="display: none;">
                    <div class="token-row">
                        <span class="token-label">Private:</span>
                        <div class="token-value" id="private-${slotId}">-</div>
                    </div>
                    <div class="token-row">
                        <span class="token-label">Public:</span>
                        <div class="token-value" id="public-${slotId}">-</div>
                    </div>
                </div>
            </div>
        `).join('');
    } catch (error) {
        console.error('Failed to load slots:', error);
        slotsContainer.innerHTML = '<p class="empty-state">Failed to load slots.</p>';
    }
}

async function allocateSlot() {
    try {
        showLoading();
        allocateBtn.disabled = true;

        const data = await apiCall('/api/allocate-slot', {
            method: 'POST'
        });

        showStatus(allocationStatus, `Slot #${data.allocated_slot} allocated successfully!`, 'success');

        // Reload state and slots
        await loadUserState();
        await loadSlots();
    } catch (error) {
        console.error('Failed to allocate slot:', error);
        showStatus(allocationStatus, `Failed to allocate slot: ${error.message}`, 'error');
        allocateBtn.disabled = false;
    } finally {
        hideLoading();
    }
}

async function generateToken(slotId) {
    try {
        showLoading();

        const data = await apiCall('/api/generate-token', {
            method: 'POST',
            body: JSON.stringify({ slot_id: slotId })
        });

        document.getElementById(`private-${slotId}`).textContent = data.private;
        document.getElementById(`public-${slotId}`).textContent = data.public;
        document.getElementById(`tokens-${slotId}`).style.display = 'block';
    } catch (error) {
        console.error('Failed to generate token:', error);
        alert(`Failed to generate token: ${error.message}`);
    } finally {
        hideLoading();
    }
}

function logout() {
    localStorage.removeItem('jwt_token');
    jwtToken = null;
    currentUser = null;
    loginSection.style.display = 'block';
    dashboardSection.style.display = 'none';
}

// Event Listeners
logoutBtn.addEventListener('click', logout);
allocateBtn.addEventListener('click', allocateSlot);

// Check for token in URL (from OAuth callback)
const urlParams = new URLSearchParams(window.location.search);
const token = urlParams.get('token');

if (token) {
    localStorage.setItem('jwt_token', token);
    jwtToken = token;
    // Clean URL
    window.history.replaceState({}, document.title, window.location.pathname);
}

// Initialize
if (jwtToken) {
    loadUserInfo();
} else {
    loginSection.style.display = 'block';
    dashboardSection.style.display = 'none';
}
