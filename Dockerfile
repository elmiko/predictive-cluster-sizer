FROM ubi9/ubi
COPY ./main ./predictive-cluster-sizer
CMD ["./predictive-cluster-sizer"]
