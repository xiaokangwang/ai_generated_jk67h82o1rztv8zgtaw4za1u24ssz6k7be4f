document.addEventListener('DOMContentLoaded', () => {
    const messageElement = document.getElementById('message');
    const loginBtn = document.getElementById('login-btn');
    const userDisplay = document.getElementById('user-display');
    const usernameSpan = document.getElementById('username');
    const logoutBtn = document.getElementById('logout-btn');

    // Fetch backend message
    fetch('http://localhost:8080/api/hello')
        .then(response => response.json())
        .then(data => {
            messageElement.textContent = data.message;
        })
        .catch(error => {
            console.error('Error fetching data:', error);
            messageElement.textContent = 'Error connecting to backend';
            messageElement.style.color = 'red';
        });

    // Function to update UI for logged-in user
    function showUser(username) {
        loginBtn.style.display = 'none';
        userDisplay.style.display = 'block';
        usernameSpan.textContent = username;
    }

    // Function to handle logout
    function logout() {
        localStorage.removeItem('token');
        loginBtn.style.display = 'inline-block';
        userDisplay.style.display = 'none';
        usernameSpan.textContent = '';
    }

    // Function to fetch user info using token
    function fetchUser(token) {
        fetch('http://localhost:8080/api/me', {
            headers: {
                'Authorization': 'Bearer ' + token
            }
        })
            .then(response => {
                if (response.status === 401) {
                    throw new Error('Unauthorized');
                }
                return response.json();
            })
            .then(data => {
                showUser(data.username);
            })
            .catch(error => {
                console.error('Error fetching user:', error);
                logout(); // Invalid token, clear it
            });
    }

    // 1. Check localStorage on load
    const storedToken = localStorage.getItem('token');
    if (storedToken) {
        fetchUser(storedToken);
    }

    // 2. Check URL for new login (token)
    const urlParams = new URLSearchParams(window.location.search);
    const tokenFromUrl = urlParams.get('token');

    if (tokenFromUrl) {
        localStorage.setItem('token', tokenFromUrl);
        fetchUser(tokenFromUrl);
        // Clean up URL
        window.history.replaceState({}, document.title, "/");
    }

    // 3. Attach logout handler
    logoutBtn.addEventListener('click', logout);
});
