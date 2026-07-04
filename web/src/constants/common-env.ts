const webConfig = {
    apiUrl:
        process.env.NEXT_PUBLIC_API_URL ||
        (process.env.NODE_ENV === 'development'
            ? process.env.NEXT_PUBLIC_DEV_BACKEND || 'http://127.0.0.1:8000'
            : ''),
    appVersion: process.env.NEXT_PUBLIC_APP_VERSION || '0.0.0',
}

export default webConfig
