
# azfunctions

[![Go Reference](https://pkg.go.dev/badge/github.com/altipla-consulting/azfunctions.svg)](https://pkg.go.dev/github.com/altipla-consulting/azfunctions)

Azure Functions framework does the routing of incoming requests, intercepts any logging and output that the function produces and returns the correct invocation result format back to Azure. It sends any error of the endpoints to Sentry and returns an improved standard error template.


## Install

```shell
go get github.com/altipla-consulting/azfunctions
```


## Contributing

You can make pull requests or create issues in GitHub. Any code you send should be formatted using `make gofmt`.


## License

[MIT License](LICENSE)
