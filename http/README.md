# http

This package implements the backend HTTP server for the Affiliate Bounty Board application.

## Core Components

### API Endpoints

The server provides several API endpoints:

- **Authentication**: Token generation and validation
- **Bounty Management**:
  - `GET /bounties` - List all bounties
  - `POST /bounties` - Create a new bounty
  - `POST /bounties/pay` - Pay a bounty
  - `POST /bounties/return` - Return a bounty to owner
  - `GET /bounties/paid` - List all paid bounties
- **Submission Handling**: Process and verify Reddit content submissions

### Middleware

The package includes middleware for:

- Authentication and authorization
- Request logging
- Error handling
- CORS support

### Temporal Integration

The HTTP server integrates with Temporal to:

- Start bounty assessment workflows
- Check workflow status
- Process callbacks from workflows

## Testing

The HTTP handlers can be tested using standard Go testing techniques. While this package doesn't currently have dedicated tests, you can run all project tests with:

```bash
make test
```

Future improvements:

- Add handler-specific tests
- Implement integration tests with a test database
- Add API endpoint documentation tests
